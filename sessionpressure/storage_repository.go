package sessionpressure

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StorageRepositoryOverride replaces the machine-wide storage admission band
// for disk-growing work whose canonical repository matches its key in
// StoragePolicy.RepositoryOverrides. The work is held only while physical
// free space is below CriticalFreeBytes. Every other repository, and work
// that runs outside any repository, keeps the machine-wide policy.
//
// It is an operator scheduling decision, not a security boundary: a caller
// that can run in the repository can claim its floor.
type StorageRepositoryOverride struct {
	CriticalFreeBytes int64  `json:"critical_free_bytes"`
	Reason            string `json:"reason,omitempty"`
}

const (
	// MinStorageRepositoryFloorBytes keeps an override from removing the
	// emergency reserve entirely.
	MinStorageRepositoryFloorBytes  int64 = 1 << 30
	maxStorageRepositoryOverrides         = 64
	maxStorageRepositoryReasonBytes       = 256
)

func validateStorageRepositoryOverrides(overrides map[string]StorageRepositoryOverride) error {
	if len(overrides) > maxStorageRepositoryOverrides {
		return fmt.Errorf("storage repository_overrides allows at most %d entries", maxStorageRepositoryOverrides)
	}
	for _, root := range sortedStorageRepositoryKeys(overrides) {
		override := overrides[root]
		if err := ValidateStorageRepositoryKey(root); err != nil {
			return err
		}
		if override.CriticalFreeBytes < MinStorageRepositoryFloorBytes {
			return fmt.Errorf("storage repository override %q critical_free_bytes must be at least %d", root, MinStorageRepositoryFloorBytes)
		}
		if len(override.Reason) > maxStorageRepositoryReasonBytes || strings.ContainsAny(override.Reason, "\r\n") {
			return fmt.Errorf("storage repository override %q reason must be one line of at most %d bytes", root, maxStorageRepositoryReasonBytes)
		}
	}
	return nil
}

// ValidateStorageRepositoryKey requires the stored form of a repository key:
// an absolute, clean path that is not the filesystem root.
func ValidateStorageRepositoryKey(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) == root {
		return fmt.Errorf("storage repository override key %q must be an absolute clean repository path", root)
	}
	return nil
}

func sortedStorageRepositoryKeys(overrides map[string]StorageRepositoryOverride) []string {
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// StorageRepositoryRoot resolves the canonical repository for work that runs
// in path. The repository is the checkout that owns the Git object store, so
// a linked worktree, or a delivery candidate whose .git/objects links to a
// repository's store, resolves to that repository. A path outside any Git
// checkout returns "" and no error.
func StorageRepositoryRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("repository path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	for {
		info, statErr := os.Lstat(filepath.Join(current, ".git"))
		if statErr == nil {
			return storageRepositoryOwner(current, info)
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil
		}
		current = parent
	}
}

func storageRepositoryOwner(top string, dotGit os.FileInfo) (string, error) {
	gitDir := filepath.Join(top, ".git")
	if !dotGit.IsDir() {
		body, err := os.ReadFile(gitDir)
		if err != nil {
			return "", err
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(body)), "gitdir:")
		if !ok || strings.TrimSpace(target) == "" {
			return "", fmt.Errorf("%s is not a gitdir link", gitDir)
		}
		gitDir = resolveGitRelative(top, strings.TrimSpace(target))
	}
	common := gitDir
	if body, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common = resolveGitRelative(gitDir, strings.TrimSpace(string(body)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	owner := top
	if objects, err := filepath.EvalSymlinks(filepath.Join(common, "objects")); err == nil &&
		filepath.Base(objects) == "objects" && filepath.Base(filepath.Dir(objects)) == ".git" {
		owner = filepath.Dir(filepath.Dir(objects))
	}
	return filepath.EvalSymlinks(owner)
}

func resolveGitRelative(base, target string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Join(base, target)
}

// StorageRepositoryOverrideFor returns the override for an already resolved
// canonical repository root.
func StorageRepositoryOverrideFor(policy StoragePolicy, repository string) (StorageRepositoryOverride, bool) {
	if repository == "" || len(policy.RepositoryOverrides) == 0 {
		return StorageRepositoryOverride{}, false
	}
	override, ok := policy.RepositoryOverrides[repository]
	return override, ok
}

// storageRepositoryAdmission applies a repository floor to a disk-growing
// class. The machine-wide diagnostic level and its hysteresis remain in the
// snapshot for observation; they do not decide this admission.
func storageRepositoryAdmission(snapshot StorageSnapshot, repository string, override StorageRepositoryOverride, source string) Admission {
	source += "+repository-override"
	free := snapshot.AvailableBytes
	if free >= override.CriticalFreeBytes {
		return Admission{Allowed: true, Level: snapshot.Level, Source: source, Snapshot: &Snapshot{Storage: snapshot}}
	}
	reason := fmt.Sprintf("storage available %.1f GiB is below repository floor %.1f GiB for %s", float64(free)/(1<<30), float64(override.CriticalFreeBytes)/(1<<30), repository)
	snapshot.ReleaseBytes = override.CriticalFreeBytes
	return Admission{Allowed: false, Level: LevelCritical, Source: source, Dimension: "storage", Reasons: []string{reason}, Snapshot: &Snapshot{Storage: snapshot}}
}

// StorageRepositoryAdmissionStatus is the read-only status view of one
// override against the current sample.
type StorageRepositoryAdmissionStatus struct {
	Repository        string `json:"repository"`
	CriticalFreeBytes int64  `json:"critical_free_bytes"`
	Reason            string `json:"reason,omitempty"`
	Exists            bool   `json:"exists"`
	// AdmitsNow is set only when the sample is available and enforcement is on.
	AdmitsNow *bool `json:"admits_now,omitempty"`
}

func StorageRepositoryAdmissionStatuses(policy StoragePolicy, snapshot StorageSnapshot) []StorageRepositoryAdmissionStatus {
	statuses := make([]StorageRepositoryAdmissionStatus, 0, len(policy.RepositoryOverrides))
	for _, root := range sortedStorageRepositoryKeys(policy.RepositoryOverrides) {
		override := policy.RepositoryOverrides[root]
		status := StorageRepositoryAdmissionStatus{Repository: root, CriticalFreeBytes: override.CriticalFreeBytes, Reason: override.Reason}
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			status.Exists = true
		}
		if snapshot.Available && policy.Enabled && policy.EnforceAdmission {
			admits := snapshot.AvailableBytes >= override.CriticalFreeBytes
			status.AdmitsNow = &admits
		}
		statuses = append(statuses, status)
	}
	return statuses
}
