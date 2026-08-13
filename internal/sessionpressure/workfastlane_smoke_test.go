package sessionpressure

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"
)

// The fast lane's whole claim is that it lets light, short work through a CPU-red
// gate that would otherwise latch it for minutes. Unit tests prove the predicate;
// only a driven run proves the wiring — that the gate actually consults it, that
// the run starts instead of latching, and that the decision reaches telemetry.
//
// Real CPU-red is not reproducible on demand, so the host probe is injected. That
// is the seam RunWorkCommand already exposes for exactly this purpose.

func fastLaneSmokeLimits() WorkLimits {
	limits := testWorkLimits()
	limits.FastLaneEnabled = true
	limits.FastLaneMaxWeight = 2
	limits.FastLaneMaxRuntimeMS = 120_000
	limits.FastLaneCoordinatedCPUCeilingPct = 50
	return limits
}

// syntheticRedAdmission reports a host pinned in CPU-only red, corroborated by
// rolling CPU so the immediate path cannot dismiss it as an uncorroborated spike.
func syntheticRedAdmission() Admission {
	return Admission{
		Allowed: false, Level: LevelRed,
		Reasons: []string{"host CPU 100.0% >= red 95.0%"},
		Snapshot: &Snapshot{
			HostCPUAvailable: true, HostCPUPercent: 100,
			HostCPURollingAvailable: true, HostCPURollingPercent: 100,
		},
	}
}

// syntheticMemoryRedAdmission is the dimension the guard was actually built for.
func syntheticMemoryRedAdmission() Admission {
	return Admission{
		Allowed: false, Level: LevelRed,
		Reasons:  []string{"host free memory 10% <= red 15%"},
		Snapshot: &Snapshot{HostCPUAvailable: true, HostCPUPercent: 100},
	}
}

// seedCalibratedClass writes enough complete lifecycles that the class has a
// measured p95 the fast lane is willing to trust.
func seedCalibratedClass(t *testing.T, dir string, class WorkClass, weight int, runtimeMS int64, count int) {
	t.Helper()
	store := NewWorkEventStore(dir)
	base := time.Now().UTC().Add(-2 * time.Hour)
	for index := range count {
		operationID := fmt.Sprintf("%032x", 0x5EED0000+index)
		leaseID := fmt.Sprintf("%032x", 0x1EA50000+index)
		at := base.Add(time.Duration(index) * time.Minute)
		for step, eventType := range []WorkEventType{WorkEventQueued, WorkEventAcquired, WorkEventStarted, WorkEventCompleted} {
			event := WorkEvent{
				SchemaVersion: WorkEventSchemaVersion,
				Timestamp:     at.Add(time.Duration(step) * time.Second),
				Event:         eventType, OperationID: operationID, Class: class, Weight: weight,
				CommandDigest: CommandShapeDigest("/usr/bin/go", 2),
			}
			if eventType != WorkEventQueued {
				event.LeaseID = leaseID
			}
			if eventType == WorkEventCompleted {
				exit := 0
				event.ExitCode = &exit
				event.RuntimeMillis = runtimeMS
				event.Outcome = "completed"
			}
			if err := store.AppendDurable(event); err != nil {
				t.Fatalf("seed %s: %v", eventType, err)
			}
		}
	}
}

// TestFastLaneAdmitsUnderSyntheticCPURed is the end-to-end proof that was missing:
// a weight-1 express job starts under a host pinned at CPU-red, instead of
// latching until the wait budget expires, and says so in telemetry.
func TestFastLaneAdmitsUnderSyntheticCPURed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NICOS_WORK_GATE_TEST_MARKER", dir+"/gate-opened")
	coordinator := NewWorkCoordinator(dir, fastLaneSmokeLimits())
	seedCalibratedClass(t, dir, WorkClassExpressTest, coordinator.Limits.ExpressTestWeight, 5_000, fastLaneMinimumSamples+2)

	// The host never recovers. Without the fast lane this run can only latch and
	// then fail when the wait budget runs out.
	code, err := RunWorkCommand(
		coordinator,
		WorkRunOptions{Class: WorkClassExpressTest, Wait: 5 * time.Second, Progress: WorkProgressQuiet, Command: []string{"/usr/bin/true"}},
		syntheticRedAdmission,
		10*time.Millisecond,
		WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, CommandFactory: testGatedWorkCommand},
	)
	if err != nil || code != 0 {
		dumped, _ := NewWorkEventStore(dir).Read(WorkEventFilter{})
		for _, event := range dumped {
			if event.OperationID < "0000000000000000000000005eed0000" || event.OperationID > "0000000000000000000000005eedffff" {
				t.Logf("event=%s outcome=%s blocker=%s prestart=%dms decision=%s",
					event.Event, event.Outcome, event.Blocker, event.PrestartMilliseconds, event.AdmissionDecision)
			}
		}
		t.Fatalf("fast lane did not admit light work under sustained CPU-red: code=%d err=%v", code, err)
	}

	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{Event: WorkEventCompleted})
	if err != nil {
		t.Fatal(err)
	}
	var admitted *WorkEvent
	for index, event := range events {
		if event.AdmissionDecision == FastLaneDecisionReason {
			admitted = &events[index]
		}
	}
	if admitted == nil {
		decisions := make([]string, 0, len(events))
		for _, event := range events {
			decisions = append(decisions, event.AdmissionDecision)
		}
		t.Fatalf("no completed event recorded %s: decisions=%v", FastLaneDecisionReason, decisions)
	}
	// The admission must not have cost a latch cycle: prestart stays small even
	// though the host reported red on every single probe.
	if admitted.PrestartMilliseconds > 3_000 {
		t.Fatalf("fast-lane admission still paid a latch wait: prestart=%dms", admitted.PrestartMilliseconds)
	}

	// Livelock guard. The post-lease loop used to re-test the raw host probe,
	// which stays red forever here, so a fast-lane admission it had already
	// granted was discarded and re-reserved on every pass — reserve → reacquire
	// → reserve at roughly a hundred persisted events per second until the wait
	// budget expired. One reservation round-trip is the most this run may need.
	all, err := NewWorkEventStore(dir).Read(WorkEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	churn := 0
	for _, event := range all {
		if event.OperationID == admitted.OperationID &&
			(event.Event == WorkEventReserved || event.Event == WorkEventReacquired) {
			churn++
		}
	}
	if churn > 2 {
		t.Fatalf("reserve/reacquire livelock: %d reservation events for one operation", churn)
	}
}

// TestFastLaneRefusesUnderSyntheticMemoryRed is the counterpart: the fast lane
// must never widen the dimension this guard exists for. A memory-red host blocks
// the same job until its wait budget expires.
func TestFastLaneRefusesUnderSyntheticMemoryRed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NICOS_WORK_GATE_TEST_MARKER", dir+"/gate-opened")
	coordinator := NewWorkCoordinator(dir, fastLaneSmokeLimits())
	seedCalibratedClass(t, dir, WorkClassExpressTest, coordinator.Limits.ExpressTestWeight, 5_000, fastLaneMinimumSamples+2)

	code, err := RunWorkCommand(
		coordinator,
		WorkRunOptions{Class: WorkClassExpressTest, Wait: 300 * time.Millisecond, Progress: WorkProgressQuiet, Command: []string{"/usr/bin/true"}},
		syntheticMemoryRedAdmission,
		10*time.Millisecond,
		WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, CommandFactory: testGatedWorkCommand},
	)
	if err == nil && code == 0 {
		t.Fatal("fast lane admitted work under memory-red; that dimension must fail closed")
	}

	events, readErr := NewWorkEventStore(dir).Read(WorkEventFilter{})
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, event := range events {
		if event.AdmissionDecision == FastLaneDecisionReason {
			t.Fatalf("memory-red run recorded a fast-lane admission: %+v", event)
		}
	}
}

// TestFastLaneStillRefusesWhenCapacityIsCommitted keeps the weighted ceiling
// authoritative under a live coordinator: with the ceiling committed, a CPU-red
// host blocks even an eligible express job.
func TestFastLaneStillRefusesWhenCapacityIsCommitted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NICOS_WORK_GATE_TEST_MARKER", dir+"/gate-opened")
	limits := fastLaneSmokeLimits()
	coordinator := NewWorkCoordinator(dir, limits)
	seedCalibratedClass(t, dir, WorkClassExpressTest, limits.ExpressTestWeight, 5_000, fastLaneMinimumSamples+2)

	// Commit the ceiling so no capacity remains for a fast-lane admission.
	held, _, err := coordinator.AcquireOperation(context.Background(), WorkClassHeavy, "000000000000000000000000000000c1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release(context.Background()) }()

	code, runErr := RunWorkCommand(
		coordinator,
		WorkRunOptions{Class: WorkClassExpressTest, Wait: 300 * time.Millisecond, Progress: WorkProgressQuiet, Command: []string{"/usr/bin/true"}},
		syntheticRedAdmission,
		10*time.Millisecond,
		WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard, CommandFactory: testGatedWorkCommand},
	)
	if runErr == nil && code == 0 {
		t.Fatal("fast lane admitted past a committed weighted ceiling")
	}
}
