package sessionpressurecmd

import (
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

type storageRepositoryPolicyRequest struct {
	set          bool
	path         string
	criticalFree int64
	reason       string
}

func parseStorageRepositoryPolicyArgs(args []string) (storageRepositoryPolicyRequest, error) {
	const usage = "storage policy repository requires set PATH --critical-free SIZE [--reason TEXT] or clear PATH"
	if len(args) < 2 || (args[0] != "set" && args[0] != "clear") {
		return storageRepositoryPolicyRequest{}, fmt.Errorf("%s", usage)
	}
	request := storageRepositoryPolicyRequest{set: args[0] == "set", path: args[1]}
	if strings.TrimSpace(request.path) == "" || strings.HasPrefix(request.path, "-") {
		return storageRepositoryPolicyRequest{}, fmt.Errorf("%s", usage)
	}
	rest := args[2:]
	if !request.set {
		if len(rest) != 0 {
			return storageRepositoryPolicyRequest{}, fmt.Errorf("storage policy repository clear accepts only PATH")
		}
		return request, nil
	}
	seen := map[string]bool{}
	for len(rest) > 0 {
		flag := rest[0]
		if (flag != "--critical-free" && flag != "--reason") || len(rest) < 2 {
			return storageRepositoryPolicyRequest{}, fmt.Errorf("%s", usage)
		}
		if seen[flag] {
			return storageRepositoryPolicyRequest{}, fmt.Errorf("storage policy repository set accepts %s only once", flag)
		}
		seen[flag] = true
		switch flag {
		case "--critical-free":
			value, err := parseStorageBytes(rest[1])
			if err != nil {
				return storageRepositoryPolicyRequest{}, err
			}
			request.criticalFree = value
		case "--reason":
			request.reason = strings.TrimSpace(rest[1])
		}
		rest = rest[2:]
	}
	if !seen["--critical-free"] {
		return storageRepositoryPolicyRequest{}, fmt.Errorf("storage policy repository set requires --critical-free SIZE")
	}
	return request, nil
}

// applyStorageRepositoryPolicy returns the updated policy and the canonical
// repository key it changed. It never mutates the input policy's map.
func applyStorageRepositoryPolicy(policy sessionpressure.Policy, request storageRepositoryPolicyRequest) (sessionpressure.Policy, string, error) {
	overrides := maps.Clone(policy.Storage.RepositoryOverrides)
	if overrides == nil {
		overrides = map[string]sessionpressure.StorageRepositoryOverride{}
	}
	if request.set {
		root, err := sessionpressure.StorageRepositoryRoot(request.path)
		if err != nil {
			return policy, "", fmt.Errorf("resolve repository %q: %w", request.path, err)
		}
		if root == "" {
			return policy, "", fmt.Errorf("%q is not inside a Git repository", request.path)
		}
		overrides[root] = sessionpressure.StorageRepositoryOverride{CriticalFreeBytes: request.criticalFree, Reason: request.reason}
		policy.Storage.RepositoryOverrides = overrides
		return policy, root, policy.Validate()
	}
	// Clearing accepts the stored key even when the repository is gone.
	key := ""
	if absolute, err := filepath.Abs(request.path); err == nil {
		if _, ok := overrides[filepath.Clean(absolute)]; ok {
			key = filepath.Clean(absolute)
		}
	}
	if key == "" {
		if root, err := sessionpressure.StorageRepositoryRoot(request.path); err == nil {
			if _, ok := overrides[root]; ok {
				key = root
			}
		}
	}
	if key == "" {
		return policy, "", fmt.Errorf("no storage repository override for %q", request.path)
	}
	delete(overrides, key)
	if len(overrides) == 0 {
		overrides = nil
	}
	policy.Storage.RepositoryOverrides = overrides
	return policy, key, policy.Validate()
}

func cmdSessionPressureStorageRepositoryPolicy(g *Flags, args []string) int {
	request, err := parseStorageRepositoryPolicyArgs(args)
	if err != nil {
		return sessionPressureError(err.Error(), 2)
	}
	dir, err := sessionpressure.DataDir()
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	mutation, err := beginPressurePolicyMutation(dir)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	defer mutation.Close()
	runtime := mutation.Runtime
	if !runtime.persisted {
		return sessionPressureError("storage policy requires initialization via session pressure policy init", 1)
	}
	updated, repository, err := applyStorageRepositoryPolicy(runtime.policy, request)
	if err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	if err := sessionpressure.SavePolicy(runtime.path, updated); err != nil {
		return sessionPressureError(err.Error(), 1)
	}
	// The resident fences relief actions on an exact persisted-policy match.
	// Reload it so a policy edit does not leave relief failing closed.
	if err := restartPressureMonitor(runtime.dir); err != nil {
		return sessionPressureError("storage repository policy saved but resident monitor did not confirm reload: "+err.Error(), 1)
	}
	action := "storage.policy.repository.clear"
	text := fmt.Sprintf("storage repository override cleared for %s\n", repository)
	if request.set {
		action = "storage.policy.repository.set"
		text = fmt.Sprintf("storage repository override %s critical_free=%.1fGiB\n", repository, bytesToGiB(request.criticalFree))
	}
	payload := map[string]any{"ok": true, "action": action, "repository": repository, "storage_policy": updated.Storage, "path": runtime.path}
	return emitPressure(g, payload, text, 0)
}
