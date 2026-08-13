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
		{"warning held", 55, LevelWarning, LevelWarning, LevelNormal, true},
		{"warning released", 60, LevelWarning, LevelNormal, LevelNormal, false},
		// P4 narrowed band: red trip 25 GiB, release 30 GiB (was 35).
		{"red held", 29, LevelRed, LevelRed, LevelWarning, true},
		{"red released to warning", 30, LevelRed, LevelWarning, LevelWarning, false},
		{"critical held", 15, LevelCritical, LevelCritical, LevelRed, true},
		{"critical released to red", 20, LevelCritical, LevelRed, LevelRed, false},
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

func TestFormatStorageDeadlockAdviceActionable(t *testing.T) {
	policy := DefaultPolicy(16 << 10).Storage
	snap := StorageSnapshot{Available: true, AvailableBytes: 28 << 30, TotalBytes: 100 << 30, Level: LevelRed}
	line := FormatStorageDeadlockAdvice(snap, policy)
	if !strings.Contains(line, "storage-red deadlock") || !strings.Contains(line, "storage plan") || !strings.Contains(line, "release=") {
		t.Fatalf("advice not actionable: %s", line)
	}
	for _, want := range []string{"skill storage-reclaim", "--auto-safe --apply", "--provider user-trash", "--provider app-updater-caches", "--provider browser-media-caches", "--provider brew-cache", "catalog skill.storage-reclaim"} {
		if !strings.Contains(line, want) {
			t.Fatalf("advice missing %q: %s", want, line)
		}
	}
	if strings.Index(line, "--auto-safe --apply") > strings.Index(line, "--provider go-build-cache") {
		t.Fatalf("go-build-cache was not last reclaim lever: %s", line)
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
