package sessionpressure

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// AgentIdentityOverlayPath is the operator overlay under the session-pressure dir.
func AgentIdentityOverlayPath(dir string) string {
	return filepath.Join(dir, agentIdentityOverlayName)
}

// LoadAgentIdentityOverlay reads and validates an optional overlay file.
// Missing file returns (nil, nil). Corrupt or unsafe content returns an error
// so the caller can fall back to defaults without expanding ownership.
func LoadAgentIdentityOverlay(path string) (*AgentIdentityOverlay, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("agent identity overlay is not a regular file")
	}
	if info.Size() > 64*1024 {
		return nil, fmt.Errorf("agent identity overlay exceeds 64KiB")
	}
	body, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 64*1024 {
		return nil, fmt.Errorf("agent identity overlay exceeds 64KiB")
	}
	var overlay AgentIdentityOverlay
	if err := json.Unmarshal(body, &overlay); err != nil {
		return nil, fmt.Errorf("decode agent identity overlay: %w", err)
	}
	if err := ValidateAgentIdentityOverlay(overlay); err != nil {
		return nil, err
	}
	return &overlay, nil
}

// ValidateAgentIdentityOverlay enforces fail-closed overlay policy.
func ValidateAgentIdentityOverlay(overlay AgentIdentityOverlay) error {
	if overlay.SchemaVersion != agentIdentitySchemaVersion {
		return fmt.Errorf("agent identity overlay schema_version must be %d", agentIdentitySchemaVersion)
	}
	if len(overlay.Rules) == 0 {
		return fmt.Errorf("agent identity overlay rules must not be empty")
	}
	if len(overlay.Rules) > 16 {
		return fmt.Errorf("agent identity overlay allows at most 16 rules")
	}
	for index, rule := range overlay.Rules {
		if err := ValidateAgentIdentityRule(rule, true); err != nil {
			return fmt.Errorf("rules[%d]: %w", index, err)
		}
	}
	return nil
}

// ValidateAgentIdentityRule validates one rule. When requireInstall is true
// (overlay), every non-exact-only expansion that could classify versioned
// binaries must bind to at least one install path so ownership cannot float
// to arbitrary processes named like an agent.
func ValidateAgentIdentityRule(rule AgentIdentityRule, requireInstall bool) error {
	agent := normalizeAgentID(rule.Agent)
	if agent == "" {
		return fmt.Errorf("invalid agent id %q", rule.Agent)
	}
	if len(rule.ExactBasenames) > 16 || len(rule.PathProbePrefixes) > 16 ||
		len(rule.InstallPathPrefixes) > 16 || len(rule.InstallPathExact) > 16 ||
		len(rule.NodeScriptBasenames) > 16 {
		return fmt.Errorf("rule list exceeds bound of 16 entries")
	}
	for _, name := range rule.ExactBasenames {
		if normalizeBasename(name) == "" || strings.Contains(name, "/") {
			return fmt.Errorf("invalid exact basename %q", name)
		}
	}
	for _, name := range rule.NodeScriptBasenames {
		if normalizeBasename(name) == "" || strings.Contains(name, "/") {
			return fmt.Errorf("invalid node script basename %q", name)
		}
	}
	for _, prefix := range rule.PathProbePrefixes {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix == "" || strings.Contains(prefix, "/") || len(prefix) > 32 {
			return fmt.Errorf("invalid path probe prefix %q", prefix)
		}
	}
	hasInstall := false
	for _, prefix := range rule.InstallPathPrefixes {
		if _, ok := normalizeHomeRelativePath(prefix, true); !ok {
			return fmt.Errorf("invalid install path prefix %q", prefix)
		}
		hasInstall = true
	}
	for _, exact := range rule.InstallPathExact {
		if _, ok := normalizeHomeRelativePath(exact, false); !ok {
			return fmt.Errorf("invalid install path exact %q", exact)
		}
		hasInstall = true
	}
	if requireInstall {
		// Overlay may only introduce ownership via trusted install paths or
		// exact basenames that are already short stable names. Path probes
		// without install roots are rejected.
		if rule.PathProbeSemVer || len(rule.PathProbePrefixes) > 0 {
			if !hasInstall {
				return fmt.Errorf("path probes require at least one install path root")
			}
		}
		if len(rule.ExactBasenames) == 0 && len(rule.NodeScriptBasenames) == 0 && !hasInstall {
			return fmt.Errorf("rule must declare basenames or install paths")
		}
	}
	return nil
}

// MergeAgentIdentityOverlay unions overlay rules into the base catalog.
func MergeAgentIdentityOverlay(base *AgentIdentityCatalog, overlay *AgentIdentityOverlay, overlayPath string) (*AgentIdentityCatalog, error) {
	if base == nil {
		base = CompileAgentIdentityCatalog(DefaultAgentIdentityRules())
	}
	if overlay == nil {
		return base, nil
	}
	if err := ValidateAgentIdentityOverlay(*overlay); err != nil {
		return nil, err
	}
	mergedRules := append([]AgentIdentityRule(nil), base.Rules...)
	mergedRules = append(mergedRules, overlay.Rules...)
	// Re-validate the combined rule set does not invent empty agents.
	for index, rule := range mergedRules {
		if err := ValidateAgentIdentityRule(rule, false); err != nil {
			return nil, fmt.Errorf("merged rules[%d]: %w", index, err)
		}
	}
	catalog := CompileAgentIdentityCatalog(mergedRules)
	catalog.OverlayLoaded = true
	catalog.OverlayPath = overlayPath
	return catalog, nil
}

func statRegularFile(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	return info, nil
}

func statPath(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func osUserHomeDir() (string, error) {
	return os.UserHomeDir()
}
