package sessionpressure

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Installed shape on 2026-11-11: critical entry 10 GiB, recovery 15 GiB,
// block_new_at critical, enforcement on.
func repositoryOverridePolicy(garrid string) Policy {
	policy := DefaultPolicy(16 << 10)
	policy.Storage.EnforceAdmission = true
	policy.Storage.BlockNewAt = LevelCritical
	policy.Storage.CriticalFreeBytes = 10 << 30
	policy.Storage.CriticalReleaseBytes = 15 << 30
	policy.Storage.RepositoryOverrides = map[string]StorageRepositoryOverride{
		garrid: {CriticalFreeBytes: 5 << 30, Reason: "garrid build floor"},
	}
	return policy
}

func storageSampleGiB(policy Policy, freeGiB int64, previous Level) StorageSnapshot {
	return EvaluateStorage(StorageSnapshot{Available: true, TotalBytes: 1000 << 30, AvailableBytes: freeGiB << 30}, policy.Storage, previous)
}

func TestStorageRepositoryOverrideScopesAdmission(t *testing.T) {
	const garrid = "/Users/example/dev/garrid"
	const other = "/Users/example/dev/other"
	policy := repositoryOverridePolicy(garrid)
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}

	// Positive: garrid at 7 GiB is below the machine-wide 10 GiB entry but
	// above its own 5 GiB floor.
	at7 := storageSampleGiB(policy, 7, LevelNormal)
	if at7.Level != LevelCritical {
		t.Fatalf("machine-wide level at 7 GiB = %s, want critical", at7.Level)
	}
	got := StorageAdmissionForRepository(at7, policy, WorkClassTest, "test", garrid)
	if !got.Allowed || !strings.Contains(got.Source, "repository-override") || got.Warning != "" {
		t.Fatalf("garrid at 7 GiB = %+v, want admitted by override", got)
	}

	// Negative: another repository, and work outside any repository, keep
	// the 10 GiB block.
	for _, repository := range []string{other, ""} {
		got := StorageAdmissionForRepository(at7, policy, WorkClassTest, "test", repository)
		if got.Allowed || pressureDimension(got) != "storage" {
			t.Fatalf("repository %q at 7 GiB = %+v, want held on storage", repository, got)
		}
		if got.Snapshot.Storage.ReleaseBytes != 15<<30 {
			t.Fatalf("repository %q release = %d, want machine-wide 15 GiB", repository, got.Snapshot.Storage.ReleaseBytes)
		}
	}

	// Negative: garrid below its own floor is held, with its floor as the
	// release target and a storage-prefixed reason.
	at4 := storageSampleGiB(policy, 4, LevelNormal)
	got = StorageAdmissionForRepository(at4, policy, WorkClassTest, "test", garrid)
	if got.Allowed || pressureDimension(got) != "storage" || got.Level != LevelCritical {
		t.Fatalf("garrid at 4 GiB = %+v, want held on storage", got)
	}
	if got.Snapshot.Storage.ReleaseBytes != 5<<30 || len(got.Reasons) != 1 || !strings.HasPrefix(got.Reasons[0], "storage ") || !strings.Contains(got.Reasons[0], garrid) {
		t.Fatalf("garrid hold evidence = %+v", got)
	}
	if advice := FormatStorageDeadlockAdvice(got.Snapshot.Storage, policy.Storage); !strings.Contains(advice, "release=5.0 GiB") {
		t.Fatalf("advice does not request the repository floor: %s", advice)
	}

	// The exact floor admits; the class split does not apply to an override.
	at5 := storageSampleGiB(policy, 5, LevelNormal)
	for _, class := range AllWorkClasses() {
		if got := StorageAdmissionForRepository(at5, policy, class, "test", garrid); !got.Allowed {
			t.Fatalf("garrid %s at the 5 GiB floor = %+v", class, got)
		}
		got := StorageAdmissionForRepository(at4, policy, class, "test", garrid)
		if got.Allowed != (class == WorkClassReclaim || !storageGrowingClass(class)) {
			t.Fatalf("garrid %s at 4 GiB allowed=%v", class, got.Allowed)
		}
	}

	// Machine-wide hysteresis does not hold garrid; it still holds others.
	latched := storageSampleGiB(policy, 12, LevelCritical)
	if !latched.HysteresisActive {
		t.Fatalf("12 GiB after critical should be latched: %+v", latched)
	}
	if got := StorageAdmissionForRepository(latched, policy, WorkClassBuild, "test", garrid); !got.Allowed {
		t.Fatalf("garrid during machine hysteresis = %+v", got)
	}
	if got := StorageAdmissionForRepository(latched, policy, WorkClassBuild, "test", other); got.Allowed {
		t.Fatalf("other repository during machine hysteresis = %+v", got)
	}

	// Observe-only policy never enforces, with or without an override.
	policy.Storage.EnforceAdmission = false
	if got := StorageAdmissionForRepository(at4, policy, WorkClassTest, "test", garrid); !got.Allowed {
		t.Fatalf("observe policy enforced the override: %+v", got)
	}
}

func TestStorageRepositoryOverrideValidation(t *testing.T) {
	for name, overrides := range map[string]map[string]StorageRepositoryOverride{
		"relative key":  {"dev/garrid": {CriticalFreeBytes: 5 << 30}},
		"unclean key":   {"/Users/example/dev/garrid/": {CriticalFreeBytes: 5 << 30}},
		"root key":      {"/": {CriticalFreeBytes: 5 << 30}},
		"empty key":     {"": {CriticalFreeBytes: 5 << 30}},
		"zero floor":    {"/Users/example/dev/garrid": {}},
		"tiny floor":    {"/Users/example/dev/garrid": {CriticalFreeBytes: MinStorageRepositoryFloorBytes - 1}},
		"multi reason":  {"/Users/example/dev/garrid": {CriticalFreeBytes: 5 << 30, Reason: "a\nb"}},
		"long reason":   {"/Users/example/dev/garrid": {CriticalFreeBytes: 5 << 30, Reason: strings.Repeat("x", 257)}},
		"dotdot in key": {"/Users/example/../garrid": {CriticalFreeBytes: 5 << 30}},
	} {
		t.Run(name, func(t *testing.T) {
			policy := DefaultPolicy(16 << 10)
			policy.Storage.RepositoryOverrides = overrides
			if err := policy.Validate(); err == nil {
				t.Fatal("invalid override validated")
			}
		})
	}
	// Overrides are validated even when storage observation is disabled.
	policy := DefaultPolicy(16 << 10)
	policy.Storage.Enabled = false
	policy.Storage.RepositoryOverrides = map[string]StorageRepositoryOverride{"relative": {CriticalFreeBytes: 5 << 30}}
	if err := policy.Validate(); err == nil {
		t.Fatal("disabled storage skipped override validation")
	}
}

func TestStorageRepositoryOverridePersistsAndCompares(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	policy := repositoryOverridePolicy("/Users/example/dev/garrid")
	if err := SavePolicy(path, policy); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"repository_overrides"`) || !strings.Contains(string(body), `"critical_free_bytes": 5368709120`) {
		t.Fatalf("persisted policy lacks the typed override:\n%s", body)
	}
	loaded, persisted, err := LoadPolicy(path, 16<<10)
	if err != nil || !persisted {
		t.Fatalf("LoadPolicy persisted=%v err=%v", persisted, err)
	}
	if !loaded.Equal(policy) {
		t.Fatalf("round trip changed policy: %+v", loaded.Storage.RepositoryOverrides)
	}
	changed := loaded
	changed.Storage.RepositoryOverrides = map[string]StorageRepositoryOverride{"/Users/example/dev/garrid": {CriticalFreeBytes: 6 << 30}}
	if changed.Equal(policy) {
		t.Fatal("Equal ignored an override change")
	}
	// A policy without overrides omits the field and stays loadable by
	// readers that predate it.
	plain := DefaultPolicy(16 << 10)
	encoded, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "repository_overrides") {
		t.Fatal("empty overrides must be omitted")
	}
	empty := plain
	empty.Storage.RepositoryOverrides = map[string]StorageRepositoryOverride{}
	if !empty.Equal(plain) {
		t.Fatal("nil and empty overrides must compare equal")
	}
	// Migrating a pre-storage policy keeps operator overrides.
	legacy := policy
	legacy.Storage.WarningFreeBytes = 0
	legacyBody, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacyBody, 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, _, err := LoadPolicy(path, 16<<10)
	if err != nil || migrated.Storage.RepositoryOverrides["/Users/example/dev/garrid"].CriticalFreeBytes != 5<<30 {
		t.Fatalf("legacy migration dropped override: %+v err=%v", migrated.Storage.RepositoryOverrides, err)
	}
}

func TestStorageRepositoryRootResolvesCheckoutWorktreeAndCandidate(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkdir := func(parts ...string) string {
		path := filepath.Join(append([]string{base}, parts...)...)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	repo := mkdir("dev", "garrid")
	mkdir("dev", "garrid", ".git", "objects")
	mkdir("dev", "garrid", ".git", "worktrees", "feature")
	nested := mkdir("dev", "garrid", "apps", "ios")

	// A linked worktree names its private git dir, whose commondir is the
	// repository's .git.
	worktree := mkdir("worktrees", "garrid-feature")
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "feature")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "worktrees", "feature", "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A commit-delivery candidate slot has its own .git whose objects link
	// to the repository's object store.
	candidate := mkdir("state", "candidates", "abc", "g", "dev", ".ndev-commit-candidate-slot0-1fa5fe26a257")
	mkdir("state", "candidates", "abc", "g", "dev", ".ndev-commit-candidate-slot0-1fa5fe26a257", ".git")
	if err := os.Symlink(filepath.Join(repo, ".git", "objects"), filepath.Join(candidate, ".git", "objects")); err != nil {
		t.Fatal(err)
	}
	candidateSub := mkdir("state", "candidates", "abc", "g", "dev", ".ndev-commit-candidate-slot0-1fa5fe26a257", "pkg")

	// An unrelated repository and a directory outside any repository.
	other := mkdir("dev", "other")
	mkdir("dev", "other", ".git", "objects")
	outside := mkdir("scratch")

	// A symlinked spelling of the repository resolves to its real path.
	alias := filepath.Join(base, "garrid-link")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path, want string
	}{
		{"checkout root", repo, repo},
		{"checkout subdirectory", nested, repo},
		{"linked worktree", worktree, repo},
		{"delivery candidate", candidate, repo},
		{"delivery candidate subdirectory", candidateSub, repo},
		{"symlinked path", alias, repo},
		{"other repository", other, other},
		{"outside any repository", outside, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := StorageRepositoryRoot(tc.path)
			if err != nil || got != tc.want {
				t.Fatalf("StorageRepositoryRoot(%s) = %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
	if _, err := StorageRepositoryRoot(filepath.Join(base, "missing")); err == nil {
		t.Fatal("missing path resolved")
	}
	broken := mkdir("broken")
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("not a gitdir link\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StorageRepositoryRoot(broken); err == nil {
		t.Fatal("malformed .git file resolved")
	}
}

func TestConfiguredStorageAdmissionAppliesRepositoryOverride(t *testing.T) {
	dir := t.TempDir()
	const garrid = "/Users/example/dev/garrid"
	if err := SavePolicy(PolicyPath(dir), repositoryOverridePolicy(garrid)); err != nil {
		t.Fatal(err)
	}
	free := int64(7 << 30)
	sampler := &Sampler{now: time.Now, storageSource: func(string) (StorageCapacity, error) {
		return StorageCapacity{TotalBytes: 1000 << 30, FreeBytes: free, AvailableBytes: free}, nil
	}}
	in := func(root string, err error) func() (string, error) {
		return func() (string, error) { return root, err }
	}

	if got := configuredStorageAdmission(dir, WorkClassTest, sampler, in(garrid, nil)); !got.Allowed {
		t.Fatalf("garrid at 7 GiB = %+v, want admitted", got)
	}
	if got := configuredStorageAdmission(dir, WorkClassTest, sampler, in("/Users/example/dev/other", nil)); got.Allowed || pressureDimension(got) != "storage" {
		t.Fatalf("other repository at 7 GiB = %+v, want held", got)
	}
	// A repository that cannot be resolved never receives an override.
	if got := configuredStorageAdmission(dir, WorkClassTest, sampler, in(garrid, errors.New("getwd failed"))); got.Allowed {
		t.Fatalf("unresolved repository at 7 GiB = %+v, want held", got)
	}
	free = 4 << 30
	if got := configuredStorageAdmission(dir, WorkClassTest, sampler, in(garrid, nil)); got.Allowed || got.Snapshot.Storage.ReleaseBytes != 5<<30 {
		t.Fatalf("garrid at 4 GiB = %+v, want held until 5 GiB", got)
	}

	// Fail closed: an unreadable or invalid persisted override holds
	// disk-growing work, and reclaim stays available.
	free = 7 << 30
	for name, body := range map[string]string{
		"corrupt":       "{not json",
		"invalid floor": strings.Replace(mustPolicyJSON(t, repositoryOverridePolicy(garrid)), "5368709120", "1", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(PolicyPath(dir), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			got := configuredStorageAdmission(dir, WorkClassTest, sampler, in(garrid, nil))
			if got.Allowed || pressureDimension(got) != "storage" || !strings.Contains(AdmissionReason(got), "failed closed") {
				t.Fatalf("unreadable override = %+v, want held", got)
			}
			if got := configuredStorageAdmission(dir, WorkClassReclaim, sampler, in(garrid, nil)); !got.Allowed {
				t.Fatalf("reclaim with unreadable policy = %+v", got)
			}
		})
	}
	// No persisted policy remains unconfigured, as before.
	if err := os.Remove(PolicyPath(dir)); err != nil {
		t.Fatal(err)
	}
	if got := configuredStorageAdmission(dir, WorkClassTest, sampler, in(garrid, nil)); !got.Allowed || got.Source != "unconfigured" {
		t.Fatalf("missing policy = %+v", got)
	}
}

func mustPolicyJSON(t *testing.T, policy Policy) string {
	t.Helper()
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestStorageRepositoryAdmissionStatuses(t *testing.T) {
	garrid := t.TempDir()
	policy := repositoryOverridePolicy(garrid)
	policy.Storage.RepositoryOverrides["/Users/example/gone"] = StorageRepositoryOverride{CriticalFreeBytes: 8 << 30}
	statuses := StorageRepositoryAdmissionStatuses(policy.Storage, storageSampleGiB(policy, 7, LevelNormal))
	if len(statuses) != 2 || statuses[0].Repository != "/Users/example/gone" || statuses[1].Repository != garrid {
		t.Fatalf("statuses = %+v", statuses)
	}
	if statuses[0].Exists || statuses[0].AdmitsNow == nil || *statuses[0].AdmitsNow {
		t.Fatalf("gone repository status = %+v", statuses[0])
	}
	if !statuses[1].Exists || statuses[1].AdmitsNow == nil || !*statuses[1].AdmitsNow || statuses[1].CriticalFreeBytes != 5<<30 {
		t.Fatalf("garrid status = %+v", statuses[1])
	}
	unavailable := StorageRepositoryAdmissionStatuses(policy.Storage, StorageSnapshot{})
	if unavailable[1].AdmitsNow != nil {
		t.Fatal("unavailable sample must not claim an admission verdict")
	}
}
