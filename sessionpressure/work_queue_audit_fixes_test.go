package sessionpressure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAttachOnlyBrowserCommandsDoNotAccumulatePhantomCharge(t *testing.T) {
	if !isAttachOnlyBrowserCommand([]string{"ndev", "browser", "snapshot", "--session", "s1"}) {
		t.Fatal("expected ndev browser snapshot to be attach-only")
	}
	if !isAttachOnlyBrowserCommand([]string{"/usr/local/bin/ndev", "--json", "browser", "click", "#go"}) {
		t.Fatal("expected ndev --json browser click to be attach-only")
	}
	if isAttachOnlyBrowserCommand([]string{"ndev", "browser", "open", "http://127.0.0.1:3000"}) {
		t.Fatal("expected ndev browser open to require full detached session charge")
	}
	if isAttachOnlyBrowserCommand([]string{"node", "scripts/csp-sweep.mjs"}) {
		t.Fatal("expected external browser script to require full detached session charge")
	}

	dir := t.TempDir()
	base := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	sampledAt := base.Add(-10 * time.Second)
	coordinator := &WorkCoordinator{Dir: dir, Limits: testWorkLimits()}
	headroom := MemoryHeadroom{SampleFresh: true, SampledAt: sampledAt, HeadroomMB: 4096}

	// 100 rapid attach-only commands between two resident samples must never
	// exhaust 4096 MB of headroom. Before this fix, 5 commands at 928 MB each
	// saturated 4096 MB and stalled the 6th command for up to 90 s.
	for i := range 100 {
		now := base.Add(time.Duration(i) * 100 * time.Millisecond)
		admitter := newBrowserAdmitterForCommand(coordinator, BrowserRuntimeChromeForTesting, 0, []string{"ndev", "browser", "snapshot"})
		admitter.now = func() time.Time { return now }
		admitter.alive = func(int) bool { return true }
		admitter.headroom = func(context.Context, time.Time) (MemoryHeadroom, error) { return headroom, nil }

		decision, admitted, err := admitter.tryAcquire(context.Background())
		if err != nil {
			t.Fatalf("attach %d tryAcquire: %v", i, err)
		}
		if !decision.Admit || admitted == nil {
			t.Fatalf("attach %d refused unexpectedly: %+v", i, decision)
		}
		if decision.CostMB != browserAttachSessionMB {
			t.Fatalf("attach %d cost_mb = %d, want %d", i, decision.CostMB, browserAttachSessionMB)
		}
		admitted.Started()
		admitted.Release()
	}

	entries, err := readBrowserAdmissions(filepath.Join(dir, browserAdmissionLedgerName))
	if err != nil {
		t.Fatalf("readBrowserAdmissions: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("released attach-only rows should prune immediately, got %d", len(entries))
	}

	// Detached launch (`ndev browser open`) must still keep its charge after
	// Release until a newer sample covers it.
	openAdmitter := newBrowserAdmitterForCommand(coordinator, BrowserRuntimeChromeForTesting, 0, []string{"ndev", "browser", "open"})
	openAdmitter.now = func() time.Time { return base.Add(20 * time.Second) }
	openAdmitter.alive = func(int) bool { return true }
	openAdmitter.headroom = func(context.Context, time.Time) (MemoryHeadroom, error) { return headroom, nil }
	decision, admitted, err := openAdmitter.tryAcquire(context.Background())
	if err != nil || !decision.Admit || admitted == nil {
		t.Fatalf("open tryAcquire failed: decision=%+v err=%v", decision, err)
	}
	if decision.CostMB != browserChromeForTestingSessionMB {
		t.Fatalf("open cost_mb = %d, want %d", decision.CostMB, browserChromeForTestingSessionMB)
	}
	admitted.Started()
	admitted.Release()

	entries, err = readBrowserAdmissions(filepath.Join(dir, browserAdmissionLedgerName))
	if err != nil {
		t.Fatalf("readBrowserAdmissions after open: %v", err)
	}
	if len(entries) != 1 || !entries[0].chargedAgainst(sampledAt) {
		t.Fatalf("detached open row should remain charged against older sample, got %+v", entries)
	}
}

func TestWaitForBrowserAdmissionPublishesAndReleasesHold(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	coordinator := &WorkCoordinator{Dir: dir, Limits: testWorkLimits()}
	admitter := newBrowserAdmitterForCommand(coordinator, BrowserRuntimeHeadlessShell, 0, []string{"ndev", "browser", "open"})
	admitter.now = func() time.Time { return now }
	admitter.alive = func(int) bool { return true }

	var attempts atomic.Int32
	admitter.headroom = func(context.Context, time.Time) (MemoryHeadroom, error) {
		if attempts.Add(1) == 1 {
			return MemoryHeadroom{SampleFresh: true, SampledAt: now, HeadroomMB: 100}, nil
		}
		return MemoryHeadroom{SampleFresh: true, SampledAt: now, HeadroomMB: 4096}, nil
	}
	var heldDim, heldReason string
	var released bool
	admitter.onHold = func(dimension, reason string) {
		heldDim = dimension
		heldReason = reason
	}
	admitter.onRelease = func() {
		released = true
	}

	admitted, err := waitForBrowserAdmission(context.Background(), true, func(context.Context) error { return nil }, admitter, nil, 5*time.Millisecond)
	if err != nil || admitted == nil {
		t.Fatalf("waitForBrowserAdmission: admitted=%v err=%v", admitted, err)
	}
	if heldDim != "memory" || !strings.Contains(heldReason, "BROWSER_NO_HEADROOM") {
		t.Fatalf("hold = (%q, %q), want memory / BROWSER_NO_HEADROOM", heldDim, heldReason)
	}
	if !released {
		t.Fatal("expected onRelease to run after browser admission succeeded")
	}
	if admitter.lastBlocker != WorkBlockerBrowserMemory || admitter.lastDimension != "memory" {
		t.Fatalf("last blocker = (%q, %q), want (%q, memory)", admitter.lastBlocker, admitter.lastDimension, WorkBlockerBrowserMemory)
	}
}

func TestWaitForWorkAdmissionWakesOnCapacityNotify(t *testing.T) {
	dir := t.TempDir()
	var checks atomic.Int32
	gate := &workAdmissionGate{
		limits:            testWorkLimits(),
		capacityNotifyDir: dir,
		memoryHeadroom: func(Admission) (ShadowMemoryDecision, int, bool) {
			if checks.Add(1) == 1 {
				return ShadowMemoryDecision{Reason: "exceeds_headroom"}, 1, true
			}
			return ShadowMemoryDecision{Admit: true, Reason: "fits"}, 0, true
		},
	}
	gate.limits.MemoryAdmission = MemoryAdmissionEnforce

	go func() {
		time.Sleep(40 * time.Millisecond)
		notifyWorkCapacity(dir)
	}()

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := waitForWorkAdmission(ctx, true, func() Admission {
		return Admission{Allowed: true, Level: LevelNormal}
	}, 15*time.Second, gate, nil, nil)
	if err != nil {
		t.Fatalf("waitForWorkAdmission failed: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("waitForWorkAdmission took %s, expected wake via capacity notify well below 15s", elapsed)
	}
}

func TestResourceAdmissionBypassesOversizedHeadWaiter(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	// 48 GiB host with 20% free -> ~9830 MB available; minus 6144 MB reserve -> ~3686 MB headroom;
	// one active lease predicting 1000 MB leaves ~2686 MB headroom.
	snapshot := Snapshot{
		Timestamp:        now,
		PhysicalMemoryMB: 49152,
		FreePercent:      20,
		Level:            LevelNormal,
	}
	if err := NewTelemetryStore(dir).WriteLatest(snapshot); err != nil {
		t.Fatalf("append telemetry: %v", err)
	}
	limits := testWorkLimits()
	limits.Admission = WorkAdmissionResource
	limits.SchedulingPolicy = WorkSchedulingPolicy

	newCoord := func(pid int, predictedPeakMB int64) *WorkCoordinator {
		return &WorkCoordinator{
			Dir: dir, Limits: limits, PID: pid,
			Now:              func() time.Time { return now },
			ProcessAlive:     func(int) bool { return true },
			ProcessIdentity:  func(int) (string, error) { return "test-id", nil },
			EventStore:       NewWorkEventStore(dir),
			SchedulingPolicy: WorkSchedulingPolicy,
			ResourceFit:      true,
			PredictedPeakMB:  predictedPeakMB,
		}
	}

	// Lease 1 runs with 1000 MB predicted peak.
	runner := newCoord(1001, 1000)
	lease1, _, err := runner.AcquireOperation(context.Background(), WorkClassBuild, "00000000000000000000000000000001")
	if err != nil {
		t.Fatalf("acquire lease1: %v", err)
	}
	defer lease1.Release(context.Background())

	// Waiter A (head) predicts 6000 MB (> ~2768 MB headroom).
	bigCoord := newCoord(1002, 6000)
	bigWaiter, _, err := bigCoord.RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000002")
	if err != nil {
		t.Fatalf("register big waiter: %v", err)
	}
	defer bigWaiter.Cancel(context.Background())

	// Waiter B (behind A) predicts 500 MB (fits ~2768 MB headroom).
	smallCoord := newCoord(1003, 500)
	smallWaiter, _, err := smallCoord.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000003")
	if err != nil {
		t.Fatalf("register small waiter: %v", err)
	}

	// Big head cannot acquire yet and returns ErrWorkResource (not ErrWorkCapacity).
	if _, _, err := bigWaiter.TryAcquire(context.Background()); !errors.Is(err, ErrWorkFairness) && !errors.Is(err, ErrWorkResource) {
		t.Fatalf("bigWaiter TryAcquire err = %v, want ErrWorkFairness or ErrWorkResource", err)
	}

	// Small waiter behind it MUST acquire immediately without waiting 20 s for
	// bigWaiter's selected-start bound to expire.
	smallLease, status, err := smallWaiter.TryAcquire(context.Background())
	if err != nil {
		t.Fatalf("smallWaiter should bypass oversized head immediately, got err: %v", err)
	}
	defer smallLease.Release(context.Background())
	if status.DecisionReason != "oldest_feasible_head_bypass" {
		t.Fatalf("decision reason = %q, want oldest_feasible_head_bypass", status.DecisionReason)
	}
	if len(status.Waiters) != 1 || status.Waiters[0].BypassCount != 1 {
		t.Fatalf("expected oversized head to accrue 1 bypass, waiters=%+v", status.Waiters)
	}
}

func TestShadowResourceProbeSkipsOwnerLookupWhenUnused(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	if err := NewTelemetryStore(dir).WriteLatest(Snapshot{
		Timestamp:        now,
		PhysicalMemoryMB: 49152,
		FreePercent:      50,
		Level:            LevelNormal,
	}); err != nil {
		t.Fatalf("append telemetry: %v", err)
	}
	var ownerCalls int
	probe := &shadowResourceProbe{
		memory: &shadowMemoryProbe{dir: dir, class: WorkClassTest, history: &peakRSSHistory{}},
		owners: func([]WorkLeaseStatus) map[string]string {
			ownerCalls++
			return nil
		},
	}
	leases := []WorkLeaseStatus{{ID: "l1", OperationID: "op1"}, {ID: "l2", OperationID: "op2"}}
	_ = probe.decide(leases, now)
	if ownerCalls != 0 {
		t.Fatalf("expected 0 owner SQLite lookups at LevelNormal, got %d", ownerCalls)
	}
}

func TestMaybePruneWorkArtifactsThrottlesByStamp(t *testing.T) {
	dir := t.TempDir()
	store := NewWorkEventStore(dir)
	if err := maybePruneWorkArtifacts(dir, store, 14, nil); err != nil {
		t.Fatalf("first maybePruneWorkArtifacts: %v", err)
	}
	stampPath := filepath.Join(dir, workPruneStampName)
	info1, err := os.Stat(stampPath)
	if err != nil {
		t.Fatalf("expected prune stamp file: %v", err)
	}
	// Create an old preview file that pruneWorkOutput would remove if it ran again.
	previewDir := filepath.Join(dir, "work-output")
	if err := os.MkdirAll(previewDir, 0700); err != nil {
		t.Fatalf("mkdir work-output: %v", err)
	}
	staleFile := filepath.Join(previewDir, "old.log")
	if err := os.WriteFile(staleFile, []byte("old"), 0600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	oldTime := time.Now().Add(-3 * time.Hour)
	_ = os.Chtimes(staleFile, oldTime, oldTime)

	// Second call within workPruneInterval must skip pruning.
	if err := maybePruneWorkArtifacts(dir, store, 14, nil); err != nil {
		t.Fatalf("second maybePruneWorkArtifacts: %v", err)
	}
	if _, err := os.Stat(staleFile); err != nil {
		t.Fatalf("expected throttled second call to skip pruning, got stat err: %v", err)
	}
	info2, _ := os.Stat(stampPath)
	if !info2.ModTime().Equal(info1.ModTime()) {
		t.Fatal("expected stamp mtime unchanged during throttle window")
	}
}
