package sessionpressure

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type resourceCleanerFunc func(context.Context, Snapshot) (ResourceCleanupResult, error)

func (fn resourceCleanerFunc) MaybeRelieve(ctx context.Context, snapshot Snapshot) (ResourceCleanupResult, error) {
	return fn(ctx, snapshot)
}

func TestRunResourceCleanupSurfacesDeduplicatedErrorAndRecovery(t *testing.T) {
	dir := t.TempDir()
	store := NewTelemetryStore(dir)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	monitor := NewMonitor(nil, store, DefaultPolicy(16<<10))
	monitor.Cleaner = resourceCleanerFunc(func(context.Context, Snapshot) (ResourceCleanupResult, error) {
		return ResourceCleanupResult{}, errors.New("claim state is corrupt")
	})
	state := resourceCleanupTelemetryState{}
	stats := monitorStats{}

	snapshot := monitor.runResourceCleanup(context.Background(), Snapshot{Timestamp: now}, true, &state, &stats)
	if snapshot.ResourceCleanupError != "claim state is corrupt" || state.lastError != snapshot.ResourceCleanupError {
		t.Fatalf("snapshot=%+v state=%+v", snapshot, state)
	}
	events, err := store.ReadEvents(10, time.Time{})
	if err != nil || len(events) != 1 || events[0].Event != "resource_cleanup_error" {
		t.Fatalf("events=%+v err=%v", events, err)
	}

	// A repeated failure inside the bounded interval stays visible in latest
	// state without amplifying the JSONL telemetry stream.
	_ = monitor.runResourceCleanup(context.Background(), Snapshot{Timestamp: now.Add(time.Minute)}, true, &state, &stats)
	if stats.resourceCleanupFailures != 2 {
		t.Fatalf("cleanup failure attempts = %d, want 2", stats.resourceCleanupFailures)
	}
	events, err = store.ReadEvents(10, time.Time{})
	if err != nil || len(events) != 1 {
		t.Fatalf("deduplicated events=%+v err=%v", events, err)
	}

	monitor.Cleaner = resourceCleanerFunc(func(context.Context, Snapshot) (ResourceCleanupResult, error) {
		return ResourceCleanupResult{}, nil
	})
	recovered := monitor.runResourceCleanup(context.Background(), Snapshot{Timestamp: now.Add(resourceCleanupTelemetryMinInterval)}, true, &state, &stats)
	if recovered.ResourceCleanupError != "" || state.lastError != "" {
		t.Fatalf("recovered snapshot=%+v state=%+v", recovered, state)
	}
	events, err = store.ReadEvents(10, time.Time{})
	if err != nil || len(events) != 2 || events[1].Event != "resource_cleanup_recovered" {
		t.Fatalf("recovery events=%+v err=%v", events, err)
	}
}

func TestRunResourceCleanupRetainsLastControlPerformance(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	monitor := &Monitor{}
	called := 0
	monitor.Cleaner = resourceCleanerFunc(func(context.Context, Snapshot) (ResourceCleanupResult, error) {
		called++
		if called == 1 {
			return ResourceCleanupResult{ControlExecuted: true, ControlDurationMS: 12.5, ControlMaxRSSMB: 41}, nil
		}
		return ResourceCleanupResult{}, nil
	})
	state := resourceCleanupTelemetryState{}
	stats := monitorStats{}
	first := monitor.runResourceCleanup(context.Background(), Snapshot{Timestamp: now}, true, &state, &stats)
	second := monitor.runResourceCleanup(context.Background(), Snapshot{Timestamp: now.Add(time.Minute)}, true, &state, &stats)
	monitor.annotateOperationalTelemetry(&second, state, stats)
	if !first.ResourceCleanupExecutedAt.Equal(now) || !second.ResourceCleanupExecutedAt.Equal(now) || second.ResourceCleanupDurationMS != 12.5 || second.ResourceCleanupMaxRSSMB != 41 {
		t.Fatalf("retained cleanup performance: first=%+v second=%+v", first, second)
	}
}

func TestRunResourceCleanupEventuallyPersistsRateLimitedTransitions(t *testing.T) {
	dir := t.TempDir()
	store := NewTelemetryStore(dir)
	now := time.Date(2026, 7, 21, 13, 0, 0, 0, time.UTC)
	monitor := NewMonitor(nil, store, DefaultPolicy(16<<10))
	failing := true
	monitor.Cleaner = resourceCleanerFunc(func(context.Context, Snapshot) (ResourceCleanupResult, error) {
		if failing {
			return ResourceCleanupResult{}, errors.New("claim state is corrupt")
		}
		return ResourceCleanupResult{}, nil
	})
	state := resourceCleanupTelemetryState{}
	stats := monitorStats{}

	_ = monitor.runResourceCleanup(context.Background(), Snapshot{Timestamp: now}, true, &state, &stats)
	failing = false
	_ = monitor.runResourceCleanup(context.Background(), Snapshot{Timestamp: now.Add(time.Minute)}, true, &state, &stats)
	if !state.pendingRecoveryEvent {
		t.Fatalf("recovery transition was not retained: %+v", state)
	}
	_ = monitor.runResourceCleanup(context.Background(), Snapshot{Timestamp: now.Add(resourceCleanupTelemetryMinInterval)}, true, &state, &stats)

	failing = true
	_ = monitor.runResourceCleanup(context.Background(), Snapshot{Timestamp: now.Add(6 * time.Minute)}, true, &state, &stats)
	if !state.pendingErrorEvent {
		t.Fatalf("error transition was not retained: %+v", state)
	}
	_ = monitor.runResourceCleanup(context.Background(), Snapshot{Timestamp: now.Add(10 * time.Minute)}, true, &state, &stats)
	events, err := store.ReadEvents(10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"resource_cleanup_error", "resource_cleanup_recovered", "resource_cleanup_error"}
	if len(events) != len(want) {
		t.Fatalf("events=%+v", events)
	}
	for index := range want {
		if events[index].Event != want[index] {
			t.Fatalf("events[%d]=%q want=%q", index, events[index].Event, want[index])
		}
	}
}

func TestAssessGuardHealthReportsResourceCleanupFailure(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	policy := DefaultPolicy(16 << 10)
	policy.EnforceAdmission = true
	policy.AutoShedCritical = true
	digest := strings.Repeat("a", 64)
	launchd := LaunchdStatus{OK: true, Installed: true, Loaded: true, PID: 42, ArtifactPresent: true, ArtifactVerified: true, ArtifactSHA256: digest}
	latest := Snapshot{
		Timestamp: now.Add(-time.Minute), Level: LevelNormal,
		ProcessInventoryAvailable: true, ProcessInventoryCapturedAt: now.Add(-time.Minute),
		GuardPID: 42, GuardRole: "resident", GuardBinarySHA256: digest,
		GuardBudgetApplicable: true, GuardBudgetOK: true, GuardBaselineProven: true,
		MonitorSamples: 4, NormalMonitorSamples: 4, ResourceCleanupError: "claim state is corrupt",
	}
	health := AssessGuardHealth(now, policy, true, launchd, latest, true)
	if health.MonitorHealthy || health.DailyDriverReady || !strings.Contains(strings.Join(health.HealthReasons, "\n"), "typed resource cleanup is failing") {
		t.Fatalf("health=%+v", health)
	}
}
