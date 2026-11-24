package sessionpressurecmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func watchProbe(free int64) storageWatchProbe {
	return storageWatchProbe{
		Storage: sessionpressure.StorageSnapshot{Available: true, AvailableBytes: free},
		Policy:  sessionpressure.StoragePolicy{WarningFreeBytes: 50 << 30, RedFreeBytes: 25 << 30},
	}
}

func conditionByID(conditions []storageWatchCondition, id string) storageWatchCondition {
	for _, condition := range conditions {
		if condition.ID == id {
			return condition
		}
	}
	return storageWatchCondition{}
}

func TestStorageWatchDiskBands(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		free int64
		want string
	}{{80 << 30, "ok"}, {50 << 30, "warning"}, {26 << 30, "warning"}, {25 << 30, "critical"}, {3 << 30, "critical"}} {
		state := storageWatchState{}
		got := conditionByID(evaluateStorageWatch(watchProbe(tc.free), &state, now), "disk-free").Severity
		if got != tc.want {
			t.Fatalf("free=%d severity=%s want %s", tc.free, got, tc.want)
		}
	}
}

func TestStorageWatchEpisodesAlertOnceEscalateAndRealert(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	state := storageWatchState{}
	alerts := func(free int64, at time.Time) int {
		return len(storageWatchDue(evaluateStorageWatch(watchProbe(free), &state, at), &state, at))
	}
	if n := alerts(40<<30, now); n != 1 {
		t.Fatalf("open warning alerts=%d", n)
	}
	if n := alerts(40<<30, now.Add(time.Hour)); n != 0 {
		t.Fatalf("repeat within 24h alerts=%d", n)
	}
	if n := alerts(20<<30, now.Add(2*time.Hour)); n != 1 {
		t.Fatalf("escalation alerts=%d", n)
	}
	if n := alerts(20<<30, now.Add(27*time.Hour)); n != 1 {
		t.Fatalf("daily re-alert alerts=%d", n)
	}
	if n := alerts(80<<30, now.Add(28*time.Hour)); n != 0 || len(state.Episodes) != 0 {
		t.Fatalf("recovery alerts=%d episodes=%v", n, state.Episodes)
	}
}

func TestStorageWatchBackupDriveAndQuota(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	state := storageWatchState{}
	probe := watchProbe(80 << 30)
	probe.DriveMounted = true
	probe.TMConfigured = true
	probe.TMLatest = now.Add(-49 * time.Hour)
	probe.TMMounted = true
	probe.TMUsedBytes, probe.TMTotalBytes = 91, 100
	conditions := evaluateStorageWatch(probe, &state, now)
	for id, want := range map[string]string{"time-machine": "warning", "x10-mounted": "ok", "nicostm-quota": "warning"} {
		if got := conditionByID(conditions, id).Severity; got != want {
			t.Fatalf("%s=%s want %s", id, got, want)
		}
	}
	probe.DriveMounted = false
	if got := conditionByID(evaluateStorageWatch(probe, &state, now.Add(23*time.Hour)), "x10-mounted").Severity; got != "ok" {
		t.Fatalf("drive within grace=%s", got)
	}
	if got := conditionByID(evaluateStorageWatch(probe, &state, now.Add(25*time.Hour)), "x10-mounted").Severity; got != "warning" {
		t.Fatalf("drive absent 25h=%s", got)
	}
	probe.TMConfigured = false
	if got := conditionByID(evaluateStorageWatch(probe, &state, now), "time-machine").Severity; got != "info" {
		t.Fatalf("unconfigured TM=%s", got)
	}
}

func TestStorageWatchRoutesToastAndScratchpad(t *testing.T) {
	originalNotify, originalScratchpad := storageWatchNotify, storageWatchScratchpad
	t.Cleanup(func() { storageWatchNotify, storageWatchScratchpad = originalNotify, originalScratchpad })
	toasts, notes := 0, ""
	storageWatchNotify = func(storageWatchCondition) error { toasts++; return nil }
	storageWatchScratchpad = func(text string) error { notes = text; return nil }
	logPath := filepath.Join(t.TempDir(), "alerts.jsonl")
	outcomes := routeStorageWatchAlerts([]storageWatchCondition{{ID: "disk-free", Severity: "critical", Summary: "low", Action: "plan"}}, logPath, time.Now())
	if toasts != 1 || notes == "" || len(outcomes) != 2 {
		t.Fatalf("toasts=%d note=%q outcomes=%v", toasts, notes, outcomes)
	}
	if data, err := os.ReadFile(logPath); err != nil || len(data) == 0 {
		t.Fatalf("alert log missing: %v", err)
	}
}

func TestStorageWatchNoteHookOpenCoreDefault(t *testing.T) {
	t.Setenv(storageWatchNoteCommandEnv, "")
	if err := defaultStorageWatchNote("hello"); err != errStorageWatchNoteUnconfigured {
		t.Fatalf("defaultStorageWatchNote without env = %v, want errStorageWatchNoteUnconfigured", err)
	}

	outFile := filepath.Join(t.TempDir(), "captured.txt")
	hookScript := filepath.Join(t.TempDir(), "capture-note.sh")
	script := "#!/bin/sh\ncat > " + outFile + "\n"
	if err := os.WriteFile(hookScript, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(storageWatchNoteCommandEnv, hookScript)
	if err := defaultStorageWatchNote("disk warning"); err != nil {
		t.Fatalf("defaultStorageWatchNote with hook failed: %v", err)
	}
	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read captured note: %v", err)
	}
	if strings.TrimSpace(string(got)) != "disk warning" {
		t.Fatalf("captured note = %q, want %q", string(got), "disk warning")
	}
}

func TestParseTMBackupTime(t *testing.T) {
	got := parseTMBackupTime("/Volumes/.timemachine/X/2026-11-16-101530.backup")
	if got.IsZero() || got.Hour() != 10 || got.Minute() != 15 {
		t.Fatalf("got %v", got)
	}
	if !parseTMBackupTime("garbage").IsZero() {
		t.Fatal("garbage parsed")
	}
}

func TestTMExcludeDiscoverStopsAtRebuildableFolders(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "dev")
	for _, dir := range []string{
		"app/node_modules/pkg/node_modules", "app/src", "rust/target/debug", "repo/.git/objects",
		"repo/.git/node_modules", "perf-b12", "web/.next/cache",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	found, _, err := tmExcludeDiscover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		filepath.Join(root, "app", "node_modules"): true, filepath.Join(root, "rust", "target"): true,
		filepath.Join(root, "perf-b12"): true, filepath.Join(root, "web", ".next"): true,
	}
	if len(found) != len(want) {
		t.Fatalf("found %v", found)
	}
	for _, path := range found {
		if !want[path] {
			t.Fatalf("unexpected %s in %v", path, found)
		}
	}
}

func TestTMExcludePlanArithmeticAndPartialMeasures(t *testing.T) {
	home := t.TempDir()
	original := tmExcludeMeasure
	t.Cleanup(func() { tmExcludeMeasure = original })
	tmExcludeMeasure = func(path string) (int64, bool, string) {
		switch {
		case strings.HasSuffix(path, "Caches"):
			return 10, true, ""
		case strings.HasSuffix(path, ".Trash"):
			return 0, true, "partial: exit status 1"
		case path == "/Applications":
			return 100, true, ""
		}
		return 0, false, ""
	}
	if err := os.MkdirAll(filepath.Join(home, "dev", "app", "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	plan := buildTMExcludePlan(context.Background(), home, 1000, 900)
	if plan.DefaultExcluded != 10 || plan.FixedBytes != 100 || plan.EstimatedIncluded != 890 || !plan.MeetsTarget {
		t.Fatalf("plan arithmetic: %+v", plan)
	}
	if plan.MeasureErrors != 1 || len(plan.Rebuildable) != 1 {
		t.Fatalf("errors=%d rebuildable=%d", plan.MeasureErrors, len(plan.Rebuildable))
	}
}
