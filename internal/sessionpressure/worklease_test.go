package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

func testWorkLimits() WorkLimits {
	return WorkLimits{
		SchedulingPolicy: WorkSchedulingPolicyFIFO,
		Capacity:         8, TestWeight: 3, BuildWeight: 4, ExpressTestWeight: 1, ExpressBuildWeight: 2,
		EmulatorWeight: 5, BrowserWeight: 2, HeavyWeight: 6, BenchmarkWeight: 6, ReclaimWeight: 1,
		CPUBlockSamples: 2, CPUReleaseSamples: 2, CPUReleasePercent: 80,
	}
}

func testWorkCoordinator(t *testing.T) *WorkCoordinator {
	t.Helper()
	return &WorkCoordinator{
		Dir: t.TempDir(), Limits: testWorkLimits(), PID: 100,
		Now:             func() time.Time { return time.Date(2026, 7, 13, 15, 0, 0, 0, time.UTC) },
		ProcessAlive:    func(pid int) bool { return pid == 100 },
		ProcessIdentity: func(pid int) (string, error) { return fmt.Sprintf("test:%d", pid), nil },
	}
}

func TestWorkStatusHonorsShortContext(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	unlock, err := filelock.Acquire(coordinator.statePath(), time.Second)
	if err != nil {
		t.Fatalf("hold lease lock: %v", err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = coordinator.Status(ctx)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Status should fail while the lease file is locked")
	}
	if elapsed >= time.Second {
		t.Fatalf("Status waited %s under a 80ms ctx; lock timeout must follow the caller deadline", elapsed)
	}
}

func TestWorkCoordinatorAppliesOneWeightedCapacityAcrossClasses(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	build, status, err := coordinator.Acquire(context.Background(), WorkClassBuild)
	if err != nil || status.Used != 4 || status.Available != 4 {
		t.Fatalf("build acquire: status=%+v err=%v", status, err)
	}
	if _, status, err := coordinator.Acquire(context.Background(), WorkClassEmulator); !errors.Is(err, ErrWorkCapacity) || status.Used != 4 {
		t.Fatalf("emulator should contend with build: status=%+v err=%v", status, err)
	}
	browser, status, err := coordinator.Acquire(context.Background(), WorkClassBrowser)
	if err != nil || status.Used != 6 || len(status.Leases) != 2 {
		t.Fatalf("browser acquire: status=%+v err=%v", status, err)
	}
	if err := browser.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := build.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	emulator, status, err := coordinator.Acquire(context.Background(), WorkClassEmulator)
	if err != nil || status.Used != 5 || status.Available != 3 {
		t.Fatalf("emulator after release: status=%+v err=%v", status, err)
	}
	if err := emulator.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = coordinator.Status(context.Background())
	if err != nil || status.Used != 0 || len(status.Leases) != 0 {
		t.Fatalf("final status: %+v err=%v", status, err)
	}
}

func TestDefaultWorkLimitsAllowBuildPlusTestAndBrowserButNotTwoBuilds(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	coordinator.Limits = defaultWorkLimits(10)
	build, status, err := coordinator.Acquire(context.Background(), WorkClassBuild)
	if err != nil || status.Used != 5 || status.Available != 3 {
		t.Fatalf("default build acquire: status=%+v err=%v", status, err)
	}
	if _, status, err := coordinator.Acquire(context.Background(), WorkClassBuild); !errors.Is(err, ErrWorkCapacity) || status.Used != 5 {
		t.Fatalf("second default build should be blocked: status=%+v err=%v", status, err)
	}
	test, status, err := coordinator.Acquire(context.Background(), WorkClassTest)
	if err != nil || status.Used != 8 || status.Available != 0 {
		t.Fatalf("default test alongside build: status=%+v err=%v", status, err)
	}
	if err := test.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	browser, status, err := coordinator.Acquire(context.Background(), WorkClassBrowser)
	if err != nil || status.Used != 7 || status.Available != 1 {
		t.Fatalf("default browser alongside build: status=%+v err=%v", status, err)
	}
	if err := browser.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := build.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBenchmarkClassLeavesResidualForExpress(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	coordinator.Limits = defaultWorkLimits(10)
	benchmark, status, err := coordinator.Acquire(context.Background(), WorkClassBenchmark)
	if err != nil || status.Used != 6 || status.Available != 2 {
		t.Fatalf("benchmark acquire: status=%+v err=%v", status, err)
	}
	express, status, err := coordinator.Acquire(context.Background(), WorkClassExpressTest)
	if err != nil || status.Used != 7 {
		t.Fatalf("express residual: status=%+v err=%v", status, err)
	}
	if err := express.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := benchmark.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBenchmarkExclusiveClassConsumesFullCapacity(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	coordinator.Limits = defaultWorkLimits(10)
	benchmark, status, err := coordinator.Acquire(context.Background(), WorkClassBenchmarkExclusive)
	if err != nil || status.Used != status.Capacity || status.Available != 0 {
		t.Fatalf("benchmark exclusive acquire: status=%+v err=%v", status, err)
	}
	if _, status, err := coordinator.Acquire(context.Background(), WorkClassReclaim); !errors.Is(err, ErrWorkCapacity) || status.Available != 0 {
		t.Fatalf("exclusive lease was not exclusive: status=%+v err=%v", status, err)
	}
	if err := benchmark.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkCoordinatorPrunesDeadOwnerBeforeAdmission(t *testing.T) {
	alive := map[int]bool{100: true}
	coordinator := testWorkCoordinator(t)
	coordinator.ProcessAlive = func(pid int) bool { return alive[pid] }
	if _, _, err := coordinator.Acquire(context.Background(), WorkClassHeavy); err != nil {
		t.Fatal(err)
	}
	alive[100] = false
	alive[200] = true
	coordinator.PID = 200
	coordinator.Now = func() time.Time { return time.Date(2026, 7, 13, 15, 1, 0, 0, time.UTC) }
	lease, status, err := coordinator.Acquire(context.Background(), WorkClassHeavy)
	if err != nil || status.Pruned != 1 || status.Used != 6 || len(status.Leases) != 1 || status.Leases[0].PID != 200 {
		t.Fatalf("stale lease recovery: status=%+v err=%v", status, err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkCoordinatorPrunesReusedPIDOwnerBeforeAdmission(t *testing.T) {
	identity := "test:100:old"
	coordinator := testWorkCoordinator(t)
	coordinator.ProcessIdentity = func(int) (string, error) { return identity, nil }
	if _, _, err := coordinator.Acquire(context.Background(), WorkClassHeavy); err != nil {
		t.Fatal(err)
	}
	// PID 100 remains live, but now names an unrelated process lifetime.
	identity = "test:100:new"
	status, err := coordinator.Status(context.Background())
	if err != nil || status.Pruned != 1 || status.Used != 0 || len(status.Leases) != 0 {
		t.Fatalf("reused PID was not reclaimed: status=%+v err=%v", status, err)
	}
}

func TestWorkCoordinatorIdentityProbeFailureFailsClosed(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	if _, _, err := coordinator.Acquire(context.Background(), WorkClassBuild); err != nil {
		t.Fatal(err)
	}
	coordinator.ProcessIdentity = func(int) (string, error) { return "", errors.New("identity probe unavailable") }
	if _, err := coordinator.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "identity probe unavailable") {
		t.Fatalf("identity failure did not fail closed: %v", err)
	}
}

func TestWorkCoordinatorPersistsPruningWhenAcquireStillCannotFit(t *testing.T) {
	alive := map[int]bool{100: true, 200: true, 300: true}
	coordinator := testWorkCoordinator(t)
	coordinator.ProcessAlive = func(pid int) bool { return alive[pid] }
	browser, _, err := coordinator.Acquire(context.Background(), WorkClassBrowser)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.PID = 200
	heavy, status, err := coordinator.Acquire(context.Background(), WorkClassHeavy)
	if err != nil || status.Used != 8 {
		t.Fatalf("fill capacity: status=%+v err=%v", status, err)
	}
	alive[100] = false
	coordinator.PID = 300
	if _, status, err = coordinator.Acquire(context.Background(), WorkClassBuild); !errors.Is(err, ErrWorkCapacity) || status.Pruned != 1 || status.Used != 6 {
		t.Fatalf("failed acquire after prune: status=%+v err=%v", status, err)
	}
	status, err = coordinator.Status(context.Background())
	if err != nil || status.Pruned != 0 || status.Used != 6 || len(status.Leases) != 1 {
		t.Fatalf("pruned state was not durable: status=%+v err=%v", status, err)
	}
	if err := heavy.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The stale browser lease was already durably removed; Release remains
	// idempotent for its in-memory handle.
	if err := browser.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkLeaseBindsCrashRecoveryToChildPID(t *testing.T) {
	alive := map[int]bool{100: true, 200: true}
	coordinator := testWorkCoordinator(t)
	coordinator.ProcessAlive = func(pid int) bool { return alive[pid] }
	lease, _, err := coordinator.Acquire(context.Background(), WorkClassBuild)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.BindPID(context.Background(), 200); err != nil {
		t.Fatal(err)
	}
	alive[100] = false
	status, err := coordinator.Status(context.Background())
	if err != nil || status.Pruned != 0 || len(status.Leases) != 1 || status.Leases[0].PID != 200 || lease.Record().PID != 200 || lease.Record().OwnerIdentity != "test:200" {
		t.Fatalf("child-bound lease was pruned with wrapper: status=%+v record=%+v err=%v", status, lease.Record(), err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkCoordinatorStateIsPrivateAndCorruptionFailsClosed(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	lease, status, err := coordinator.Acquire(context.Background(), WorkClassBuild)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(status.StatePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode=%v err=%v", info.Mode().Perm(), err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(status.StatePath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.Acquire(context.Background(), WorkClassBuild); err == nil {
		t.Fatal("corrupt coordinator state unexpectedly admitted heavy work")
	}
}

func TestWorkStatusOmitsPrivateOwnerIdentity(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	_, status, err := coordinator.Acquire(context.Background(), WorkClassBuild)
	if err != nil {
		t.Fatal(err)
	}
	publicJSON, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicJSON), "owner_identity") {
		t.Fatalf("public status leaked private process identity: %s", publicJSON)
	}
	privateJSON, err := os.ReadFile(status.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(privateJSON), `"owner_identity": "test:100"`) {
		t.Fatalf("private state omitted process identity: %s", privateJSON)
	}
}

func TestWorkCoordinatorReconcilesPolicyWeightBeforeAdmission(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	startedAt := coordinator.Now().UTC()
	state := workState{
		SchemaVersion: workStateSchemaVersion,
		Leases: []WorkLeaseRecord{{
			ID: "00000000000000000000000000000001", OperationID: "00000000000000000000000000000011", Class: WorkClassBuild,
			Weight: 1, PID: 100, OwnerIdentity: "test:100", QueuedAt: startedAt, StartedAt: startedAt,
		}},
	}
	if err := coordinator.writeState(state); err != nil {
		t.Fatal(err)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil || status.Used != coordinator.Limits.BuildWeight || status.Available != coordinator.Limits.Capacity-coordinator.Limits.BuildWeight || status.Leases[0].Weight != coordinator.Limits.BuildWeight {
		t.Fatalf("stale weight was not reconciled: status=%+v err=%v", status, err)
	}
	persisted, err := coordinator.readState()
	if err != nil || len(persisted.Leases) != 1 || persisted.Leases[0].Weight != coordinator.Limits.BuildWeight {
		t.Fatalf("reconciled weight was not durable: state=%+v err=%v", persisted, err)
	}
}

func TestWorkCoordinatorSemanticCorruptionFailsClosed(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	state := workState{
		SchemaVersion: workStateSchemaVersion,
		Leases: []WorkLeaseRecord{{
			ID: "00000000000000000000000000000001", OperationID: "00000000000000000000000000000011", Class: WorkClass("compile-ish"),
			Weight: 1, PID: 100, OwnerIdentity: "test:100", QueuedAt: coordinator.Now().UTC(), StartedAt: coordinator.Now().UTC(),
		}},
	}
	if err := coordinator.writeState(state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.Acquire(context.Background(), WorkClassBrowser); err == nil || !strings.Contains(err.Error(), "validate work lease state") {
		t.Fatalf("semantic corruption unexpectedly admitted work: %v", err)
	}
}

func TestWorkCoordinatorMigratesLegacyPIDOnlyLeaseConservatively(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	legacy := fmt.Sprintf(`{
  "schema_version": 1,
  "leases": [{
    "id": "00000000000000000000000000000001",
    "class": "build",
    "weight": 1,
    "pid": 100,
    "started_at": %q
  }]
}
`, coordinator.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(coordinator.statePath(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil || status.SchemaVersion != 1 || status.Used != 4 || len(status.Leases) != 1 {
		t.Fatalf("live legacy compatibility: status=%+v err=%v", status, err)
	}
	raw, err := os.ReadFile(coordinator.statePath())
	if err != nil || !strings.Contains(string(raw), `"schema_version": 1`) || strings.Contains(string(raw), "owner_identity") || !strings.Contains(string(raw), `"weight": 4`) {
		t.Fatalf("schema 1 compatibility write changed its wire shape: body=%s err=%v", raw, err)
	}
	persisted, err := coordinator.readState()
	if err != nil || persisted.SchemaVersion != 1 || persisted.migrated || !persisted.legacyActive || persisted.Leases[0].OwnerIdentity != legacyWorkOwnerIdentityToken || persisted.Leases[0].OperationID != persisted.Leases[0].ID {
		t.Fatalf("live legacy state was not preserved: state=%+v err=%v", persisted, err)
	}
	if _, _, err := coordinator.AcquireOperation(context.Background(), WorkClassTest, "00000000000000000000000000000002"); !errors.Is(err, ErrWorkUpgradePending) {
		t.Fatalf("new acquisition did not wait for live legacy owner: %v", err)
	}
	coordinator.ProcessAlive = func(int) bool { return false }
	status, err = coordinator.Status(context.Background())
	if err != nil || status.SchemaVersion != 1 || status.Pruned != 1 || len(status.Leases) != 0 {
		t.Fatalf("completed legacy drain: status=%+v err=%v", status, err)
	}
	persisted, err = coordinator.readState()
	if err != nil || persisted.SchemaVersion != 1 || persisted.migrated || persisted.legacyActive || len(persisted.Leases) != 0 {
		t.Fatalf("empty legacy schema was not preserved: state=%+v err=%v", persisted, err)
	}
	coordinator.ProcessAlive = func(int) bool { return true }
	lease, status, err := coordinator.AcquireOperation(context.Background(), WorkClassTest, "00000000000000000000000000000003")
	if err != nil || status.SchemaVersion != workStateSchemaVersion {
		t.Fatalf("successful mutation did not promote schema: status=%+v err=%v", status, err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkCoordinatorPreservesActiveSchemaTwoUntilLegacyOwnerExits(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	legacy := fmt.Sprintf(`{
  "schema_version": 2,
  "leases": [{
    "id": "00000000000000000000000000000001",
    "class": "build",
    "weight": 1,
    "pid": 100,
    "owner_identity": "test:100",
    "started_at": %q
  }]
}
`, coordinator.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(coordinator.statePath(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil || status.SchemaVersion != 2 || status.Used != 4 || len(status.Leases) != 1 {
		t.Fatalf("live schema 2 compatibility: status=%+v err=%v", status, err)
	}
	raw, err := os.ReadFile(coordinator.statePath())
	if err != nil || !strings.Contains(string(raw), `"schema_version": 2`) || strings.Contains(string(raw), "operation_id") || strings.Contains(string(raw), "supervisor_pid") || !strings.Contains(string(raw), `"weight": 4`) {
		t.Fatalf("schema 2 compatibility write changed its wire shape: body=%s err=%v", raw, err)
	}
	if _, _, err := coordinator.AcquireOperation(context.Background(), WorkClassTest, "00000000000000000000000000000002"); !errors.Is(err, ErrWorkUpgradePending) {
		t.Fatalf("new acquisition did not wait for live schema 2 owner: %v", err)
	}
	coordinator.ProcessAlive = func(int) bool { return false }
	status, err = coordinator.Status(context.Background())
	if err != nil || status.SchemaVersion != 2 || status.Pruned != 1 || len(status.Leases) != 0 {
		t.Fatalf("schema 2 drain after owner exit: status=%+v err=%v", status, err)
	}
	coordinator.ProcessAlive = func(int) bool { return true }
	lease, status, err := coordinator.AcquireOperation(context.Background(), WorkClassTest, "00000000000000000000000000000003")
	if err != nil || status.SchemaVersion != workStateSchemaVersion {
		t.Fatalf("schema 2 successful mutation did not promote: status=%+v err=%v", status, err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkClassParsingAndWeightsAreTyped(t *testing.T) {
	for _, value := range []string{"test", "build", "express-test", "express-build", "emulator", "browser", "heavy", "benchmark", "benchmark-exclusive", "reclaim"} {
		class, err := ParseWorkClass(value)
		if err != nil {
			t.Fatal(err)
		}
		if weight, err := testWorkLimits().Weight(class); err != nil || weight < 1 {
			t.Fatalf("class=%s weight=%d err=%v", class, weight, err)
		}
	}
	if _, err := ParseWorkClass("compile-ish"); err == nil {
		t.Fatal("unknown class unexpectedly accepted")
	}
}

func TestWorkCoordinatorFIFOQueuePreventsSmallerWorkFromSkipping(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	alive := func(pid int) bool { return pid == 100 || pid == 200 }
	identity := func(pid int) (string, error) { return fmt.Sprintf("owner:%d", pid), nil }
	coordinatorA := &WorkCoordinator{
		Dir: dir, Limits: testWorkLimits(), PID: 100, Now: func() time.Time { return now },
		ProcessAlive: alive, ProcessIdentity: identity, EventStore: NewWorkEventStore(dir),
	}
	coordinatorB := &WorkCoordinator{
		Dir: dir, Limits: testWorkLimits(), PID: 200, Now: func() time.Time { return now },
		ProcessAlive: alive, ProcessIdentity: identity, EventStore: NewWorkEventStore(dir),
	}
	firstID := "00000000000000000000000000000001"
	secondID := "00000000000000000000000000000002"
	first, status, err := coordinatorA.RegisterWaiter(context.Background(), WorkClassBuild, firstID)
	if err != nil || status.QueueDepth != 1 || status.Waiters[0].Position != 1 {
		t.Fatalf("first waiter: status=%+v err=%v", status, err)
	}
	second, status, err := coordinatorB.RegisterWaiter(context.Background(), WorkClassBrowser, secondID)
	if err != nil || status.QueueDepth != 2 || status.Waiters[1].OperationID != secondID {
		t.Fatalf("second waiter: status=%+v err=%v", status, err)
	}
	if lease, blocked, err := second.TryAcquire(context.Background()); lease != nil || !errors.Is(err, ErrWorkFairness) || waiterPosition(blocked, secondID) != 2 {
		t.Fatalf("younger waiter skipped FIFO: lease=%+v status=%+v err=%v", lease, blocked, err)
	}
	firstLease, status, err := first.TryAcquire(context.Background())
	if err != nil || firstLease == nil || status.QueueDepth != 1 || status.Used != testWorkLimits().BuildWeight {
		t.Fatalf("first acquire: lease=%+v status=%+v err=%v", firstLease, status, err)
	}
	if err := firstLease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondLease, status, err := second.TryAcquire(context.Background())
	if err != nil || secondLease == nil || status.QueueDepth != 0 || status.Used != testWorkLimits().BrowserWeight {
		t.Fatalf("second acquire: lease=%+v status=%+v err=%v", secondLease, status, err)
	}
	if err := secondLease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkCoordinatorBoundedLookaheadFillsCapacityThenProtectsOldestWhenFeasible(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 16, 0, 0, 0, time.UTC)
	alive := func(pid int) bool { return pid == 100 || pid == 200 || pid == 300 }
	identity := func(pid int) (string, error) { return fmt.Sprintf("owner:%d", pid), nil }
	makeCoordinator := func(pid int) *WorkCoordinator {
		return &WorkCoordinator{Dir: dir, Limits: testWorkLimits(), PID: pid, Now: func() time.Time { return now }, ProcessAlive: alive, ProcessIdentity: identity, EventStore: NewWorkEventStore(dir), SchedulingPolicy: WorkSchedulingPolicy}
	}
	active, _, err := makeCoordinator(300).AcquireOperation(context.Background(), WorkClassEmulator, "00000000000000000000000000000030")
	if err != nil {
		t.Fatal(err)
	}
	build, _, err := makeCoordinator(100).RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < workMaximumBypasses; index++ {
		operationID := fmt.Sprintf("%032x", index+2)
		browser, _, registerErr := makeCoordinator(200).RegisterWaiter(context.Background(), WorkClassBrowser, operationID)
		if registerErr != nil {
			t.Fatal(registerErr)
		}
		lease, status, acquireErr := browser.TryAcquire(context.Background())
		if acquireErr != nil || lease == nil || status.Waiters[0].BypassCount < 1 {
			t.Fatalf("lookahead operation=%s lease=%+v status=%+v err=%v", operationID, lease, status, acquireErr)
		}
		if err := lease.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	status := mustWorkStatus(t, makeCoordinator(100))
	if status.Waiters[0].BypassCount != workMaximumBypasses || !status.Waiters[0].Protected || status.ProtectedOperationID == "" {
		t.Fatalf("oldest waiter was not protected after bounded bypasses: %+v", status)
	}
	blockedBrowser, _, err := makeCoordinator(200).RegisterWaiter(context.Background(), WorkClassBrowser, fmt.Sprintf("%032x", workMaximumBypasses+2))
	if err != nil {
		t.Fatal(err)
	}
	lease, draining, acquireErr := blockedBrowser.TryAcquire(context.Background())
	if !errors.Is(acquireErr, ErrWorkReservation) || lease != nil || draining.ProtectedOperationID != "00000000000000000000000000000001" || draining.DecisionReason != "protected_bounded_drain" {
		t.Fatalf("protected waiter did not reserve a bounded drain: lease=%+v status=%+v err=%v", lease, draining, acquireErr)
	}
	if err := active.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	buildLease, status, err := build.TryAcquire(context.Background())
	if err != nil || buildLease == nil || status.SelectedOperationID != "00000000000000000000000000000001" {
		t.Fatalf("protected build did not acquire when capacity opened: lease=%+v status=%+v err=%v", buildLease, status, err)
	}
}

func TestWorkCoordinatorBypassAgeStartsAtFirstBypass(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 16, 0, 0, 0, time.UTC)
	alive := func(pid int) bool { return pid == 100 || pid == 200 || pid == 300 }
	identity := func(pid int) (string, error) { return fmt.Sprintf("owner:%d", pid), nil }
	makeCoordinator := func(pid int) *WorkCoordinator {
		return &WorkCoordinator{Dir: dir, Limits: testWorkLimits(), PID: pid, Now: func() time.Time { return now }, ProcessAlive: alive, ProcessIdentity: identity, EventStore: NewWorkEventStore(dir), SchedulingPolicy: WorkSchedulingPolicy}
	}
	active, _, err := makeCoordinator(300).AcquireOperation(context.Background(), WorkClassEmulator, "00000000000000000000000000000030")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = active.Release(context.Background()) }()
	if _, _, err := makeCoordinator(100).RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000001"); err != nil {
		t.Fatal(err)
	}
	for index, advance := range []time.Duration{0, 20 * time.Second} {
		now = now.Add(advance)
		browser, _, registerErr := makeCoordinator(200).RegisterWaiter(context.Background(), WorkClassBrowser, fmt.Sprintf("%032x", index+2))
		if registerErr != nil {
			t.Fatal(registerErr)
		}
		lease, _, acquireErr := browser.TryAcquire(context.Background())
		if acquireErr != nil || lease == nil {
			t.Fatalf("bypass %d lease=%+v err=%v", index+1, lease, acquireErr)
		}
		if err := lease.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	state, err := makeCoordinator(100).readState()
	if err != nil || len(state.Waiters) != 1 || state.Waiters[0].LastBypassedAt == nil {
		t.Fatalf("state after bypasses=%+v err=%v", state, err)
	}
	firstBypass := time.Date(2026, 7, 14, 16, 0, 0, 0, time.UTC)
	if !state.Waiters[0].LastBypassedAt.Equal(firstBypass) {
		t.Fatalf("first bypass timestamp slid to %s", state.Waiters[0].LastBypassedAt)
	}
	now = firstBypass.Add(WorkReservationAge)
	status := mustWorkStatus(t, makeCoordinator(100))
	if !status.Waiters[0].Protected || status.Waiters[0].ProtectionReason != "bypass_age" {
		t.Fatalf("first-bypass age did not protect head: %+v", status)
	}
}

func TestWorkCoordinatorFIFOEmitsBoundedLookaheadShadowWithoutCutover(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 16, 30, 0, 0, time.UTC)
	alive := func(pid int) bool { return pid == 100 || pid == 200 || pid == 300 }
	identity := func(pid int) (string, error) { return fmt.Sprintf("owner:%d", pid), nil }
	makeCoordinator := func(pid int) *WorkCoordinator {
		return &WorkCoordinator{Dir: dir, Limits: testWorkLimits(), PID: pid, Now: func() time.Time { return now }, ProcessAlive: alive, ProcessIdentity: identity, EventStore: NewWorkEventStore(dir)}
	}
	if _, _, err := makeCoordinator(300).AcquireOperation(context.Background(), WorkClassEmulator, "00000000000000000000000000000030"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := makeCoordinator(100).RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000001"); err != nil {
		t.Fatal(err)
	}
	browser, _, err := makeCoordinator(200).RegisterWaiter(context.Background(), WorkClassBrowser, "00000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}
	lease, status, err := browser.TryAcquire(context.Background())
	if lease != nil || !errors.Is(err, ErrWorkCapacity) {
		t.Fatalf("FIFO unexpectedly cut over: lease=%+v status=%+v err=%v", lease, status, err)
	}
	if status.SchedulingPolicy != WorkSchedulingPolicyFIFO || status.CandidateSchedulingPolicy != WorkSchedulingPolicy || status.SelectedOperationID != "" || status.ShadowSelectedOperationID != "00000000000000000000000000000002" || status.ShadowDecisionReason != "oldest_feasible_head_bypass" {
		t.Fatalf("shadow decision not exposed truthfully: %+v", status)
	}
}

func TestWorkSelectorProtectsWaitAgeAndBoundsScan(t *testing.T) {
	now := time.Date(2026, 7, 14, 16, 0, 0, 0, time.UTC)
	waiters := make([]WorkWaiterRecord, workSelectorScanLimit+1)
	for index := range waiters {
		waiters[index] = WorkWaiterRecord{OperationID: fmt.Sprintf("%032x", index+1), Weight: 9, QueuedAt: now.Add(-time.Second)}
	}
	waiters[len(waiters)-1].Weight = 1
	selection := selectWorkWaiter(waiters, 0, 8, now, workGreenExpressWindow{}, 0)
	if selection.SelectedOperationID != "" || selection.DecisionReason != "no_waiter_fits_capacity" {
		t.Fatalf("selector scanned beyond its hard bound: %+v", selection)
	}
	lastBypassedAt := now.Add(-WorkReservationAge)
	waiters[0].LastBypassedAt = &lastBypassedAt
	waiters[0].BypassCount = 1
	selection = selectWorkWaiter(waiters, 0, 8, now, workGreenExpressWindow{}, 0)
	if selection.ProtectedOperationID != waiters[0].OperationID || selection.SelectedOperationID != "" || selection.DecisionReason != "protected_bounded_drain" {
		t.Fatalf("aged waiter with no feasible successor did not reserve capacity: %+v", selection)
	}
}

func TestWorkSelectorImmediatelyDrainsForCapacitySizedHead(t *testing.T) {
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	waiters := []WorkWaiterRecord{
		{OperationID: "00000000000000000000000000000001", Class: WorkClassBenchmark, Weight: 8, QueuedAt: now},
		{OperationID: "00000000000000000000000000000002", Class: WorkClassBrowser, Weight: 2, QueuedAt: now.Add(time.Second)},
	}
	selection := selectWorkWaiter(waiters, 3, 8, now, workGreenExpressWindow{}, 0)
	if selection.SelectedOperationID != "" || selection.ProtectedOperationID != waiters[0].OperationID || selection.DecisionReason != "protected_exclusive_drain" || len(selection.BypassedIndexes) != 0 {
		t.Fatalf("exclusive head did not reserve a capacity drain: %+v", selection)
	}
	selection = selectWorkWaiter(waiters, 0, 8, now, workGreenExpressWindow{}, 0)
	if selection.SelectedOperationID != waiters[0].OperationID || selection.ProtectedOperationID != waiters[0].OperationID || selection.DecisionReason != "protected_exclusive_capacity" {
		t.Fatalf("exclusive head did not acquire the drained capacity: %+v", selection)
	}
}

func TestWorkSelectorRandomizedModelIsDeterministicAndCapacitySafe(t *testing.T) {
	random := rand.New(rand.NewSource(42))
	now := time.Date(2026, 7, 14, 17, 0, 0, 0, time.UTC)
	for iteration := 0; iteration < 2000; iteration++ {
		capacity := 1 + random.Intn(12)
		used := random.Intn(capacity + 1)
		count := random.Intn(80)
		waiters := make([]WorkWaiterRecord, count)
		for index := range waiters {
			waiters[index] = WorkWaiterRecord{
				OperationID: fmt.Sprintf("%032x", index+1), Weight: 1 + random.Intn(12),
				QueuedAt: now.Add(-time.Duration(random.Intn(29)) * time.Second), BypassCount: random.Intn(workMaximumBypasses),
			}
		}
		first := selectWorkWaiter(waiters, used, capacity, now, workGreenExpressWindow{}, 0)
		second := selectWorkWaiter(waiters, used, capacity, now, workGreenExpressWindow{}, 0)
		if fmt.Sprintf("%+v", first) != fmt.Sprintf("%+v", second) {
			t.Fatalf("iteration %d selector was nondeterministic: first=%+v second=%+v", iteration, first, second)
		}
		if first.SelectedOperationID != "" {
			selectedWeight := 0
			selectedIndex := -1
			for index, waiter := range waiters {
				if waiter.OperationID == first.SelectedOperationID {
					selectedWeight = waiter.Weight
					selectedIndex = index
					break
				}
			}
			if selectedIndex < 0 || used+selectedWeight > capacity || selectedIndex >= workSelectorScanLimit {
				t.Fatalf("iteration %d unsafe selection: used=%d capacity=%d selection=%+v waiters=%+v", iteration, used, capacity, first, waiters)
			}
		}
		if first.ProtectedOperationID != "" && first.SelectedOperationID != "" && first.ProtectedOperationID != first.SelectedOperationID {
			t.Fatalf("iteration %d bypassed a feasible protected waiter: %+v", iteration, first)
		}
	}
}

func TestWorkCoordinatorPreservesSchemaThreeQueueUntilLegacyWaiterExits(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	legacy := fmt.Sprintf(`{
  "schema_version": 3,
  "leases": [],
  "waiters": [{
    "operation_id": "00000000000000000000000000000001",
    "class": "build",
    "weight": 4,
    "pid": 100,
    "owner_identity": "test:100",
    "queued_at": %q,
    "heartbeat_at": %q
  }]
}
`, coordinator.Now().UTC().Format(time.RFC3339Nano), coordinator.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(coordinator.statePath(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil || status.SchemaVersion != 3 || status.QueueDepth != 1 {
		t.Fatalf("schema-3 waiter was not preserved: status=%+v err=%v", status, err)
	}
	raw, err := os.ReadFile(coordinator.statePath())
	if err != nil || strings.Contains(string(raw), "bypass_count") || !strings.Contains(string(raw), `"schema_version": 3`) {
		t.Fatalf("schema-3 compatibility shape changed: %s err=%v", raw, err)
	}
	if _, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBrowser, "00000000000000000000000000000002"); !errors.Is(err, ErrWorkUpgradePending) {
		t.Fatalf("schema-4 mutation did not wait for schema-3 waiter: %v", err)
	}
	coordinator.ProcessAlive = func(int) bool { return false }
	status, err = coordinator.Status(context.Background())
	if err != nil || status.SchemaVersion != 3 || status.PrunedWaiters != 1 || status.QueueDepth != 0 {
		t.Fatalf("schema-3 queue did not drain compatibly: status=%+v err=%v", status, err)
	}
	coordinator.ProcessAlive = func(int) bool { return true }
	waiter, status, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000003")
	if err != nil || status.SchemaVersion != workStateSchemaVersion {
		t.Fatalf("schema-3 successful mutation did not promote: status=%+v err=%v", status, err)
	}
	if err := waiter.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkCoordinatorPreservesSchemaFourQueueUntilLegacyWaiterExits(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	now := coordinator.Now().UTC()
	legacy := fmt.Sprintf(`{
  "schema_version": 4,
  "leases": [],
  "waiters": [{
    "operation_id": "00000000000000000000000000000001",
    "class": "build",
    "weight": 4,
    "pid": 100,
    "owner_identity": "test:100",
    "queued_at": %q,
    "heartbeat_at": %q,
    "bypass_count": 2,
    "last_bypassed_at": %q
  }]
}
`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err := os.WriteFile(coordinator.statePath(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil || status.SchemaVersion != 4 || status.QueueDepth != 1 || status.Waiters[0].BypassCount != 2 {
		t.Fatalf("schema-4 waiter was not preserved: status=%+v err=%v", status, err)
	}
	raw, err := os.ReadFile(coordinator.statePath())
	if err != nil || !strings.Contains(string(raw), `"schema_version": 4`) || !strings.Contains(string(raw), `"bypass_count": 2`) || strings.Contains(string(raw), "override_operation_id") {
		t.Fatalf("schema-4 compatibility shape changed: %s err=%v", raw, err)
	}
	if _, _, err := coordinator.OverrideWaiter(context.Background(), "00000000000000000000000000000001"); !errors.Is(err, ErrWorkUpgradePending) {
		t.Fatalf("schema-5 override did not wait for schema-4 waiter: %v", err)
	}
	events, err := NewWorkEventStore(coordinator.Dir).Read(WorkEventFilter{Event: WorkEventOverrideRequested})
	if err != nil || len(events) != 0 {
		t.Fatalf("upgrade-blocked override emitted audit intent: events=%+v err=%v", events, err)
	}
	coordinator.ProcessAlive = func(int) bool { return false }
	status, err = coordinator.Status(context.Background())
	if err != nil || status.SchemaVersion != 4 || status.PrunedWaiters != 1 || status.QueueDepth != 0 {
		t.Fatalf("schema-4 queue did not drain compatibly: status=%+v err=%v", status, err)
	}
	coordinator.ProcessAlive = func(int) bool { return true }
	waiter, status, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000003")
	if err != nil || status.SchemaVersion != workStateSchemaVersion {
		t.Fatalf("schema-4 successful mutation did not promote: status=%+v err=%v", status, err)
	}
	if err := waiter.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkCoordinatorPrunesReusedWaiterPIDAndRecordsRecovery(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	operationID := "00000000000000000000000000000001"
	coordinator := &WorkCoordinator{
		Dir: dir, Limits: testWorkLimits(), PID: 100, Now: func() time.Time { return now },
		ProcessAlive:    func(int) bool { return true },
		ProcessIdentity: func(int) (string, error) { return "new-owner", nil },
		EventStore:      NewWorkEventStore(dir),
	}
	coordinator.EventStore.Now = func() time.Time { return now }
	state := workState{SchemaVersion: workStateSchemaVersion, Waiters: []WorkWaiterRecord{{
		OperationID: operationID, Class: WorkClassTest, Weight: testWorkLimits().TestWeight,
		PID: 200, OwnerIdentity: "old-owner", QueuedAt: now.Add(-time.Minute), HeartbeatAt: now.Add(-time.Second),
	}}}
	if err := coordinator.writeState(state); err != nil {
		t.Fatal(err)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil || status.PrunedWaiters != 1 || status.QueueDepth != 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	events, err := coordinator.EventStore.Read(WorkEventFilter{})
	if err != nil || len(events) != 1 || events[0].Event != WorkEventExpired || events[0].Outcome != "reused_waiter_pid" || events[0].OperationID != operationID {
		t.Fatalf("recovery events=%+v err=%v", events, err)
	}
}

func TestWorkWaiterCancellationIsIdempotentAndDurable(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	operationID := "00000000000000000000000000000001"
	waiter, status, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, operationID)
	if err != nil || status.QueueDepth != 1 {
		t.Fatalf("register status=%+v err=%v", status, err)
	}
	if err := waiter.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := waiter.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = coordinator.Status(context.Background())
	if err != nil || status.QueueDepth != 0 {
		t.Fatalf("cancel status=%+v err=%v", status, err)
	}
}

func TestWorkLeasePressureReservationReleasesCapacityAndPreservesQueueAge(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	operationID := "00000000000000000000000000000001"
	lease, _, err := coordinator.AcquireOperation(context.Background(), WorkClassBuild, operationID)
	if err != nil {
		t.Fatal(err)
	}
	originalQueuedAt := lease.Record().QueuedAt
	coordinator.Now = func() time.Time { return originalQueuedAt.Add(10 * time.Second) }
	waiter, status, err := lease.ReserveForPressure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Used != 0 || status.Available != status.Capacity || status.PressureReservationCount != 1 || status.ReservedWeight != coordinator.Limits.BuildWeight {
		t.Fatalf("pressure reservation still consumed capacity: %+v", status)
	}
	if len(status.Waiters) != 1 || status.Waiters[0].ReservationKind != WorkReservationPressure || !status.Waiters[0].Protected || status.Waiters[0].ProtectionReason != "pressure_reservation" || !status.Waiters[0].QueuedAt.Equal(originalQueuedAt) {
		t.Fatalf("pressure reservation lost queue protection: %+v", status.Waiters)
	}
	other, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBrowser, "00000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if _, blocked, err := other.TryAcquire(context.Background()); !errors.Is(err, ErrWorkReservation) || blocked.ProtectedOperationID != operationID {
		t.Fatalf("successor bypassed pressure reservation: status=%+v err=%v", blocked, err)
	}
	reacquired, status, err := waiter.TryAcquire(context.Background())
	if err != nil || status.Used != coordinator.Limits.BuildWeight || status.PressureReservationCount != 0 {
		t.Fatalf("pressure reservation did not reacquire: status=%+v err=%v", status, err)
	}
	if err := reacquired.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := other.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkLeasePressureReservationFallsBackForActiveSchemaFive(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	now := coordinator.Now().UTC()
	operationID := "00000000000000000000000000000001"
	leaseID := "00000000000000000000000000000011"
	legacy := fmt.Sprintf(`{
  "schema_version": 5,
  "leases": [{
    "id": %q,
    "operation_id": %q,
    "class": "build",
    "weight": 4,
    "pid": 100,
    "owner_identity": "test:100",
    "supervisor_pid": 100,
    "supervisor_identity": "test:100",
    "started_at": %q
  }],
  "waiters": []
}
`, leaseID, operationID, now.Format(time.RFC3339Nano))
	if err := os.WriteFile(coordinator.statePath(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	lease := &WorkLease{coordinator: coordinator, record: WorkLeaseRecord{
		ID: leaseID, OperationID: operationID, Class: WorkClassBuild, Weight: 4,
		PID: 100, OwnerIdentity: "test:100", SupervisorPID: 100, SupervisorIdentity: "test:100", QueuedAt: now, StartedAt: now,
	}}
	if _, status, err := lease.ReserveForPressure(context.Background()); !errors.Is(err, ErrWorkUpgradePending) || status.SchemaVersion != 5 || status.Used != 4 {
		t.Fatalf("active schema-five fallback status=%+v err=%v", status, err)
	}
	raw, err := os.ReadFile(coordinator.statePath())
	if err != nil || !strings.Contains(string(raw), `"schema_version": 5`) || strings.Contains(string(raw), "reservation_kind") {
		t.Fatalf("schema-five fallback changed wire shape: %s err=%v", raw, err)
	}
}

func TestWorkStatusNeverExposesPrivateOwnerIdentity(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	lease, _, err := coordinator.AcquireOperation(context.Background(), WorkClassTest, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(mustWorkStatus(t, coordinator))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "owner_identity") || strings.Contains(string(body), "test:100") {
		t.Fatalf("public status leaked owner identity: %s", body)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkStatusReviewsLongFiniteLeaseWithoutRevokingIt(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	now := coordinator.Now()
	coordinator.Now = func() time.Time { return now }
	lease, _, err := coordinator.AcquireOperation(context.Background(), WorkClassTest, "00000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(WorkFiniteLeaseReviewAge + time.Second)
	status := mustWorkStatus(t, coordinator)
	if len(status.Leases) != 1 || !status.Leases[0].Review || status.Leases[0].AgeMS <= 0 || !strings.Contains(status.Leases[0].ReviewReason, "ndev dev") {
		t.Fatalf("long finite lease should remain active with review guidance: %#v", status.Leases)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func mustWorkStatus(t *testing.T, coordinator *WorkCoordinator) WorkStatus {
	t.Helper()
	status, err := coordinator.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func greenTestSnapshot(now time.Time) Snapshot {
	return Snapshot{
		SchemaVersion: SchemaVersion, Timestamp: now, Level: LevelNormal,
		HostCPUPercent: 25, MemoryMomentum: MemoryMomentumSteady, GuardBudgetOK: true,
	}
}

func TestGreenExpressWindowRequiresFreshNormalEvidence(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	now := coordinator.Now()
	if coordinator.greenExpressWindow().Active {
		t.Fatal("missing snapshot must deactivate the window")
	}
	store := NewTelemetryStore(coordinator.Dir)
	write := func(mutate func(*Snapshot)) {
		t.Helper()
		snapshot := greenTestSnapshot(now)
		if mutate != nil {
			mutate(&snapshot)
		}
		if err := store.WriteLatest(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	write(nil)
	if got := coordinator.greenExpressWindow(); !got.Active || got.Overcommit != workGreenExpressOvercommit {
		t.Fatalf("fresh normal snapshot must activate: %+v", got)
	}
	for name, mutate := range map[string]func(*Snapshot){
		"warning-level":               func(s *Snapshot) { s.Level = LevelWarning },
		"high-cpu":                    func(s *Snapshot) { s.HostCPUPercent = workGreenExpressMaxHostCPU },
		"rapid-decline":               func(s *Snapshot) { s.MemoryMomentum = MemoryMomentumRapidDecline },
		"guard-budget-after-baseline": func(s *Snapshot) { s.GuardBudgetOK = false; s.GuardBaselineProven = true },
		"stale-timestamp":             func(s *Snapshot) { s.Timestamp = now.Add(-workGreenExpressMaxSampleAge - time.Second) },
	} {
		write(mutate)
		if coordinator.greenExpressWindow().Active {
			t.Fatalf("%s must deactivate the window", name)
		}
	}
	write(func(s *Snapshot) { s.GuardBudgetOK = false })
	if got := coordinator.greenExpressWindow(); !got.Active || got.Overcommit != 0 {
		t.Fatalf("warm-up budget failure must keep only residual express lane: %+v", got)
	}
	write(nil)
	t.Setenv("NDEV_PRESSURE_NO_GREEN_EXPRESS", "1")
	if coordinator.greenExpressWindow().Active {
		t.Fatal("kill-switch must deactivate the window")
	}
}

func TestSelectWorkWaiterGreenExpressDrainRideThrough(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	queued := now.Add(-time.Minute)
	green := workGreenExpressWindow{Active: true, Overcommit: 2}
	waiters := []WorkWaiterRecord{
		{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Class: WorkClassBuild, Weight: 5, QueuedAt: queued, BypassCount: workMaximumBypasses},
		{OperationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Class: WorkClassExpressTest, Weight: 1, QueuedAt: queued.Add(time.Second)},
	}
	// Without green evidence the drain idles available units (existing rule).
	if s := selectWorkWaiter(waiters, 6, 8, now, workGreenExpressWindow{}, 0); s.SelectedOperationID != "" || s.DecisionReason != "protected_bounded_drain" {
		t.Fatalf("no-green drain selection=%+v", s)
	}
	// Verified green: the express waiter rides genuinely idle units because
	// express weight + protected head weight fit inside capacity together.
	s := selectWorkWaiter(waiters, 6, 8, now, green, 0)
	if s.SelectedOperationID != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || s.DecisionReason != "green_express_drain_ride" {
		t.Fatalf("green ride selection=%+v", s)
	}
	if s.ProtectedOperationID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("protected head must remain visible: %+v", s)
	}
	// An express whose weight plus the head cannot fit inside capacity could
	// extend the drain, so it never rides.
	extending := []WorkWaiterRecord{
		{OperationID: "cccccccccccccccccccccccccccccccc", Class: WorkClassHeavy, Weight: 7, QueuedAt: queued, BypassCount: workMaximumBypasses},
		{OperationID: "dddddddddddddddddddddddddddddddd", Class: WorkClassExpressBuild, Weight: 2, QueuedAt: queued.Add(time.Second)},
	}
	if s := selectWorkWaiter(extending, 6, 8, now, green, 0); s.SelectedOperationID != "" || s.DecisionReason != "protected_bounded_drain" {
		t.Fatalf("drain-extending express must not ride: %+v", s)
	}
	// Exclusive clean-host drains never admit ride-throughs, green or not.
	exclusive := []WorkWaiterRecord{
		{OperationID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", Class: WorkClassBenchmarkExclusive, Weight: 8, QueuedAt: queued},
		{OperationID: "ffffffffffffffffffffffffffffffff", Class: WorkClassExpressTest, Weight: 1, QueuedAt: queued.Add(time.Second)},
	}
	if s := selectWorkWaiter(exclusive, 2, 8, now, green, 0); s.SelectedOperationID != "" || s.DecisionReason != "protected_exclusive_drain" {
		t.Fatalf("exclusive drain must stay clean: %+v", s)
	}
}

func TestSelectWorkWaiterGreenExpressOvercommit(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	queued := now.Add(-time.Second)
	green := workGreenExpressWindow{Active: true, Overcommit: 2}
	express := []WorkWaiterRecord{{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Class: WorkClassExpressTest, Weight: 1, QueuedAt: queued}}
	// Full ledger: green admits express within capacity+overcommit.
	if s := selectWorkWaiter(express, 8, 8, now, green, 0); s.SelectedOperationID == "" || s.DecisionReason != "green_express_overcommit" {
		t.Fatalf("green overcommit selection=%+v", s)
	}
	// Without green the same waiter blocks.
	if s := selectWorkWaiter(express, 8, 8, now, workGreenExpressWindow{}, 0); s.SelectedOperationID != "" {
		t.Fatalf("no-green overcommit must block: %+v", s)
	}
	// Stacked overcommit self-limits at capacity+overcommit total weight.
	if s := selectWorkWaiter(express, 10, 8, now, green, 0); s.SelectedOperationID != "" {
		t.Fatalf("stacked overcommit must block: %+v", s)
	}
	// Non-express classes never overcommit.
	build := []WorkWaiterRecord{{OperationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Class: WorkClassBuild, Weight: 4, QueuedAt: queued}}
	if s := selectWorkWaiter(build, 8, 8, now, green, 0); s.SelectedOperationID != "" {
		t.Fatalf("non-express overcommit must block: %+v", s)
	}
}

func TestAcquireGreenExpressOvercommitFastPathAndFIFOHead(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	if err := NewTelemetryStore(coordinator.Dir).WriteLatest(greenTestSnapshot(coordinator.Now())); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, _, err := coordinator.Acquire(ctx, WorkClassBuild)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release(ctx)
	second, _, err := coordinator.Acquire(ctx, WorkClassBuild)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release(ctx)
	// Ledger is 8/8: express admits through the bounded green overcommit.
	express, status, err := coordinator.Acquire(ctx, WorkClassExpressTest)
	if err != nil {
		t.Fatalf("green express fast path: status=%+v err=%v", status, err)
	}
	defer express.Release(ctx)
	if status.Used != 9 {
		t.Fatalf("used=%d want 9", status.Used)
	}
	// Non-express work still fails on capacity even at green.
	if _, _, err := coordinator.Acquire(ctx, WorkClassTest); !errors.Is(err, ErrWorkCapacity) {
		t.Fatalf("full-weight overcommit must stay blocked: %v", err)
	}
	// A second express would exceed capacity+overcommit and must block.
	if _, _, err := coordinator.Acquire(ctx, WorkClassExpressBuild); !errors.Is(err, ErrWorkCapacity) {
		t.Fatalf("stacked express overcommit must stay blocked: %v", err)
	}
}

func TestSelectWorkWaiterGreenRideCumulativeBudgetPreventsStarvation(t *testing.T) {
	// Refutation scenario from the 2026-07-20 adversarial review: a sustained
	// stream of weight-1 express riders must not keep a protected weight-6
	// head starved by consuming every freed unit. The ride budget is
	// cumulative: expressLeased + candidate + head must fit inside capacity,
	// so riders can only ever hold the residue the head can never use.
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	queued := now.Add(-time.Minute)
	green := workGreenExpressWindow{Active: true, Overcommit: 2}
	waiters := []WorkWaiterRecord{
		{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Class: WorkClassHeavy, Weight: 6, QueuedAt: queued, BypassCount: workMaximumBypasses},
		{OperationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Class: WorkClassExpressTest, Weight: 1, QueuedAt: queued.Add(time.Second)},
	}
	// First rider: no express in flight, 0+1+6 <= 8 → rides.
	if s := selectWorkWaiter(waiters, 6, 8, now, green, 0); s.DecisionReason != "green_express_drain_ride" {
		t.Fatalf("first rider must ride: %+v", s)
	}
	// Second rider: 1+1+6 <= 8 → rides.
	if s := selectWorkWaiter(waiters, 7, 8, now, green, 1); s.DecisionReason != "green_express_drain_ride" {
		t.Fatalf("second rider must ride: %+v", s)
	}
	// Post-release refutation state (used=5 after a weight-3 lease freed, two
	// riders in flight): 2+1+6 > 8 → the freed units stay idle for the head.
	if s := selectWorkWaiter(waiters, 5, 8, now, green, 2); s.SelectedOperationID != "" || s.DecisionReason != "protected_bounded_drain" {
		t.Fatalf("budget-exhausted rider must not ride: %+v", s)
	}
	// Once every non-express lease has released (used == riders == 2), the
	// head fits and admits despite the riders still running.
	if s := selectWorkWaiter(waiters, 2, 8, now, green, 2); s.SelectedOperationID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("head must admit once non-express drains: %+v", s)
	}
}
