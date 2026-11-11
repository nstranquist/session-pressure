package sessionpressure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvaluateStorageThresholdsAndReleaseHysteresis(t *testing.T) {
	policy := DefaultPolicy(16 << 10).Storage
	sample := func(gib int64) StorageSnapshot {
		return StorageSnapshot{Available: true, TotalBytes: 1000 << 30, AvailableBytes: gib << 30}
	}
	for _, tc := range []struct {
		name     string
		freeGiB  int64
		previous Level
		want     Level
		instant  Level
		latched  bool
	}{
		{"normal", 70, LevelNormal, LevelNormal, LevelNormal, false},
		{"warning entry", 49, LevelNormal, LevelWarning, LevelWarning, false},
		{"red entry", 24, LevelNormal, LevelRed, LevelRed, false},
		{"critical entry", 9, LevelNormal, LevelCritical, LevelCritical, false},
		{"critical entry boundary", 10, LevelNormal, LevelRed, LevelRed, false},
		{"warning held", 55, LevelWarning, LevelWarning, LevelNormal, true},
		{"warning released", 60, LevelWarning, LevelNormal, LevelNormal, false},
		// P4 narrowed band: red trip 25 GiB, release 30 GiB (was 35).
		{"red held", 29, LevelRed, LevelRed, LevelWarning, true},
		{"red released to warning", 30, LevelRed, LevelWarning, LevelWarning, false},
		{"critical held", 14, LevelCritical, LevelCritical, LevelRed, true},
		{"critical released to red", 15, LevelCritical, LevelRed, LevelRed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateStorage(sample(tc.freeGiB), policy, tc.previous)
			if got.Level != tc.want {
				t.Fatalf("level = %s, want %s", got.Level, tc.want)
			}
			if got.InstantaneousLevel != tc.instant || got.HysteresisActive != tc.latched {
				t.Fatalf("instant=%s latched=%v want instant=%s latched=%v", got.InstantaneousLevel, got.HysteresisActive, tc.instant, tc.latched)
			}
			if got.HysteresisActive && (got.ReleaseBytes == 0 || !strings.Contains(got.Reasons[0], "latched until")) {
				t.Fatalf("latched storage lacks release evidence: %+v", got)
			}
			if got.Level != LevelNormal && (len(got.Reasons) != 1 || !strings.HasPrefix(got.Reasons[0], "storage ")) {
				t.Fatalf("reasons = %#v", got.Reasons)
			}
		})
	}
}

func TestStorageSamplerIsConstantCostAndFailOpen(t *testing.T) {
	now := time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC)
	calls := 0
	sampler := &Sampler{now: func() time.Time { return now }, storageSource: func(path string) (StorageCapacity, error) {
		calls++
		return StorageCapacity{TotalBytes: 100 << 30, FreeBytes: 20 << 30, AvailableBytes: 20 << 30}, nil
	}}
	got := sampler.sampleStorage(DefaultPolicy(16<<10), LevelNormal)
	if calls != 1 || got.Level != LevelRed || got.AvailableBytes != 20<<30 {
		t.Fatalf("sample = %+v calls=%d", got, calls)
	}
	sampler.storageSource = func(string) (StorageCapacity, error) { return StorageCapacity{}, os.ErrPermission }
	got = sampler.sampleStorage(DefaultPolicy(16<<10), LevelRed)
	if got.Available || got.Level != LevelNormal || !strings.Contains(got.Error, "permission") {
		t.Fatalf("failed sample = %+v", got)
	}
}

func TestNativeStorageCapacitySamplesTemporaryVolume(t *testing.T) {
	got := SampleStorageCapacity(t.TempDir(), time.Now())
	if !got.Available || got.TotalBytes <= 0 || got.AvailableBytes <= 0 || got.AvailableBytes > got.TotalBytes || got.Source != "statfs" {
		t.Fatalf("sample = %+v", got)
	}
}

func TestLoadPolicyMigratesStorageObserveOnly(t *testing.T) {
	policy := DefaultPolicy(16 << 10)
	policy.Storage = StoragePolicy{}
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, persisted, err := LoadPolicy(path, 16<<10)
	if err != nil || !persisted {
		t.Fatalf("LoadPolicy persisted=%v err=%v", persisted, err)
	}
	if !got.Storage.Enabled || got.Storage.EnforceAdmission || got.Storage.WarningFreeBytes != 50<<30 || got.WorkLimits.ReclaimWeight != 1 {
		t.Fatalf("migrated storage = %+v work=%+v", got.Storage, got.WorkLimits)
	}
}

func TestStorageAdmissionIsClassAwareAndReclaimAlwaysAvailable(t *testing.T) {
	policy := DefaultPolicy(16 << 10)
	policy.Storage.EnforceAdmission = true
	sample := EvaluateStorage(StorageSnapshot{Available: true, TotalBytes: 100 << 30, AvailableBytes: 20 << 30}, policy.Storage, LevelNormal)
	if got := StorageAdmissionForClass(sample, policy, WorkClassBuild, "test"); got.Allowed || got.Level != LevelRed {
		t.Fatalf("build admission = %+v", got)
	}
	if got := StorageAdmissionForClass(sample, policy, WorkClassReclaim, "test"); !got.Allowed {
		t.Fatalf("reclaim admission = %+v", got)
	}
	// P2: install admits under storage-red.
	if got := StorageAdmissionForClass(sample, policy, WorkClassInstall, "test"); !got.Allowed {
		t.Fatalf("install admission = %+v", got)
	}
	// P3: express is sanctioned degraded path under storage-red.
	if got := StorageAdmissionForClass(sample, policy, WorkClassExpressTest, "test"); !got.Allowed {
		t.Fatalf("express-test admission under storage-red = %+v", got)
	}
	if got := StorageAdmissionForClass(sample, policy, WorkClassExpressBuild, "test"); !got.Allowed {
		t.Fatalf("express-build admission under storage-red = %+v", got)
	}
	if !ExpressAdmittedUnderStorageRed() {
		t.Fatal("P3 decision: express must admit under storage-red")
	}
	failed := StorageSnapshot{Available: false, Error: "probe failed"}
	if got := StorageAdmissionForClass(failed, policy, WorkClassBuild, "test"); !got.Allowed || !strings.Contains(got.Warning, "failed open") {
		t.Fatalf("failed probe admission = %+v", got)
	}
}

func TestStorageCriticalReserveAndClassSpecificRecovery(t *testing.T) {
	policy := DefaultPolicy(16 << 10)
	policy.Storage.EnforceAdmission = true
	for _, class := range AllWorkClasses() {
		for _, tc := range []struct {
			name     string
			free     int64
			previous Level
		}{
			{"critical entry", 9 << 30, LevelNormal},
			{"critical latched", 14 << 30, LevelCritical},
		} {
			t.Run(string(class)+"/"+tc.name, func(t *testing.T) {
				snapshot := EvaluateStorage(StorageSnapshot{Available: true, TotalBytes: 100 << 30, AvailableBytes: tc.free}, policy.Storage, tc.previous)
				got := StorageAdmissionForClass(snapshot, policy, class, "test")
				if got.Allowed != (class == WorkClassReclaim) {
					t.Fatalf("critical admission = %+v", got)
				}
				if !got.Allowed {
					want := int64(30 << 30)
					if storageRedDegradedClass(class) {
						want = 15 << 30
					}
					if got.Snapshot.Storage.ReleaseBytes != want {
						t.Fatalf("release=%d, want %d", got.Snapshot.Storage.ReleaseBytes, want)
					}
				}
			})
		}
		for _, free := range []int64{20 << 30, 29 << 30, 30 << 30} {
			snapshot := EvaluateStorage(StorageSnapshot{Available: true, TotalBytes: 100 << 30, AvailableBytes: free}, policy.Storage, LevelRed)
			got := StorageAdmissionForClass(snapshot, policy, class, "test")
			want := class == WorkClassReclaim || storageRedDegradedClass(class) || free >= 30<<30
			if got.Allowed != want {
				t.Fatalf("%s at %d: allowed=%v want %v", class, free, got.Allowed, want)
			}
		}
	}
	// Custom recovery bands need not be ordered across severity. A broad
	// build cannot recover before a larger critical latch has cleared.
	policy.Storage.CriticalReleaseBytes = 40 << 30
	critical := EvaluateStorage(StorageSnapshot{Available: true, AvailableBytes: 9 << 30}, policy.Storage, LevelNormal)
	if got := StorageAdmissionForClass(critical, policy, WorkClassBuild, "test"); got.Snapshot.Storage.ReleaseBytes != 40<<30 {
		t.Fatalf("custom critical latch omitted: %+v", got)
	}
	for _, enabled := range []bool{false, true} {
		policy.Storage.Enabled = enabled
		policy.Storage.EnforceAdmission = false
		got := StorageAdmissionForClass(StorageSnapshot{Available: true, Level: LevelCritical}, policy, WorkClassInstall, "test")
		if !got.Allowed {
			t.Fatal("observe/disabled policy must not enforce")
		}
	}
}

func TestFormatStorageDeadlockAdviceActionable(t *testing.T) {
	policy := DefaultPolicy(16 << 10).Storage
	policy.TargetFreeBytes = 60 << 30
	snap := StorageSnapshot{Available: true, AvailableBytes: 28 << 30, TotalBytes: 100 << 30, Level: LevelRed}
	line := FormatStorageDeadlockAdvice(snap, policy)
	for _, want := range []string{"physical_free=28.0 GiB (30.1 GB)", "release=30.0 GiB", "need≈2.0 GiB", "cleanup target=60.0 GiB is optional", "storage plan --target-free 30GiB", "skill storage-reclaim", "review provider eligibility"} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q: %s", want, line)
		}
	}
	for _, unwanted := range []string{"--apply", "--full-wipe", "--provider user-trash", "--provider ios-simulator-runtime", "--target-free 50GiB"} {
		if strings.Contains(line, unwanted) {
			t.Fatalf("unsafe or excessive advice %q: %s", unwanted, line)
		}
	}
	// A class-specific admission carries the actual required release; a
	// critical express job needs 15 GiB, not the broad-work 30 GiB.
	snap.AvailableBytes = 9 << 30
	snap.ReleaseBytes = 15 << 30
	if line = FormatStorageDeadlockAdvice(snap, policy); !strings.Contains(line, "--target-free 15GiB") || !strings.Contains(line, "need≈6.0 GiB") {
		t.Fatal(line)
	}
	// Custom byte thresholds must not be rounded down in copyable commands.
	snap.ReleaseBytes = (31 << 30) + 7
	if line = FormatStorageDeadlockAdvice(snap, policy); !strings.Contains(line, "--target-free 33285996551") {
		t.Fatal(line)
	}
	if line = FormatStorageDeadlockAdvice(StorageSnapshot{}, policy); strings.Contains(line, "need≈") || !strings.Contains(line, "capacity unavailable") {
		t.Fatal(line)
	}
}

func TestDefaultStorageRedReleaseHysteresisNarrowed(t *testing.T) {
	p := DefaultPolicy(16 << 10)
	// P4: release band is 5 GiB (25→30), not 10 GiB (25→35).
	if p.Storage.RedFreeBytes != 25<<30 || p.Storage.RedReleaseBytes != 30<<30 {
		t.Fatalf("red free=%d release=%d", p.Storage.RedFreeBytes, p.Storage.RedReleaseBytes)
	}
	if p.Storage.RedReleaseBytes-p.Storage.RedFreeBytes != 5<<30 {
		t.Fatalf("hysteresis band want 5GiB got %d", p.Storage.RedReleaseBytes-p.Storage.RedFreeBytes)
	}
}

func TestLoadPolicyDoesNotSilentlyMigrateCriticalRecovery(t *testing.T) {
	p := DefaultPolicy(16 << 10)
	p.Storage.CriticalReleaseBytes = 20 << 30
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	got, persisted, err := LoadPolicy(path, 16<<10)
	if err != nil || !persisted || got.Storage.CriticalReleaseBytes != 20<<30 {
		t.Fatalf("existing policy silently changed: %+v %v", got.Storage, err)
	}
}
