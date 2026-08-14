package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

const (
	holdTestOperationA = "000000000000000000000000000000a1"
	holdTestOperationB = "000000000000000000000000000000b2"
)

// TestAdmissionHoldIsVisibleWithoutChargingCapacity is the whole point of the
// record: before it existed the guard could block seven agents while reporting an
// empty queue and idle capacity.
func TestAdmissionHoldIsVisibleWithoutChargingCapacity(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	now := time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC)
	coordinator.Now = func() time.Time { return now }

	if err := coordinator.HoldAdmission(context.Background(), holdTestOperationA, WorkClassExpressTest, WorkAdmissionObservation{
		Red: true, Dimension: "cpu", Reason: "host CPU 100.0% >= red 95.0%",
	}); err != nil {
		t.Fatal(err)
	}
	status, err := coordinator.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.AdmissionHoldCount != 1 || len(status.AdmissionHolds) != 1 {
		t.Fatalf("hold not surfaced: %+v", status.AdmissionHolds)
	}
	hold := status.AdmissionHolds[0]
	if hold.OperationID != holdTestOperationA || hold.Class != WorkClassExpressTest || hold.Dimension != "cpu" {
		t.Fatalf("hold=%+v", hold)
	}
	// A hold is evidence, never a reservation.
	if status.Used != 0 || status.Available != status.Capacity || status.QueueDepth != 0 {
		t.Fatalf("hold charged capacity: used=%d available=%d queue=%d", status.Used, status.Available, status.QueueDepth)
	}

	// held_for_ms must track elapsed time so a watching operator sees it grow.
	coordinator.Now = func() time.Time { return now.Add(90 * time.Second) }
	status, err = coordinator.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.AdmissionHolds[0].HeldForMS != 90000 || status.LongestAdmissionHold != 90000 {
		t.Fatalf("held_for_ms=%d longest=%d", status.AdmissionHolds[0].HeldForMS, status.LongestAdmissionHold)
	}

	if err := coordinator.ReleaseAdmission(context.Background(), holdTestOperationA); err != nil {
		t.Fatal(err)
	}
	status, err = coordinator.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.AdmissionHoldCount != 0 || len(status.AdmissionHolds) != 0 {
		t.Fatalf("hold survived release: %+v", status.AdmissionHolds)
	}
	// Releasing an absent hold is safe so callers can defer unconditionally.
	if err := coordinator.ReleaseAdmission(context.Background(), holdTestOperationA); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
}

// TestAdmissionHoldPrunesDeadAndReusedOwners keeps the record honest: an orphan
// hold would report a block that is not happening.
func TestAdmissionHoldPrunesDeadAndReusedOwners(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		alive func(int) bool
		ident func(int) (string, error)
	}{
		{"dead owner", func(int) bool { return false }, func(pid int) (string, error) { return fmt.Sprintf("test:%d", pid), nil }},
		{"reused pid", func(int) bool { return true }, func(pid int) (string, error) { return fmt.Sprintf("recycled:%d", pid), nil }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			coordinator := testWorkCoordinator(t)
			if err := coordinator.HoldAdmission(context.Background(), holdTestOperationA, WorkClassBuild, WorkAdmissionObservation{Red: true}); err != nil {
				t.Fatal(err)
			}
			coordinator.ProcessAlive = testCase.alive
			coordinator.ProcessIdentity = testCase.ident
			status, err := coordinator.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if status.AdmissionHoldCount != 0 {
				t.Fatalf("orphan hold retained: %+v", status.AdmissionHolds)
			}
			if status.PrunedAdmissionHolds != 1 {
				t.Fatalf("pruned_admission_holds=%d", status.PrunedAdmissionHolds)
			}
			// Hold pruning must not be reported as queue churn.
			if status.PrunedWaiters != 0 {
				t.Fatalf("hold pruning leaked into pruned_waiters=%d", status.PrunedWaiters)
			}
		})
	}
}

// TestSharedAdmissionLatchCountsByTimeNotByObserver is the fix for N waiters each
// building a private latch: ten pollers must not satisfy a two-sample sustain
// requirement ten times faster than one.
func TestSharedAdmissionLatchCountsByTimeNotByObserver(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	now := time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC)
	coordinator.Now = func() time.Time { return now }
	red := WorkAdmissionObservation{Red: true, Dimension: "cpu", Reason: "host CPU 99.0% >= red 95.0%"}

	latch, err := coordinator.ObserveAdmissionLatch(context.Background(), red)
	if err != nil {
		t.Fatal(err)
	}
	if latch.Latched || latch.RedSamples != 1 {
		t.Fatalf("first red sample latched too early: %+v", latch)
	}
	// A second waiter polling at the same instant must not advance the counter.
	for range 5 {
		latch, err = coordinator.ObserveAdmissionLatch(context.Background(), red)
		if err != nil {
			t.Fatal(err)
		}
	}
	if latch.Latched || latch.RedSamples != 1 {
		t.Fatalf("concurrent observers advanced the shared counter: %+v", latch)
	}

	coordinator.Now = func() time.Time { return now.Add(2 * workAdmissionLatchSampleInterval) }
	latch, err = coordinator.ObserveAdmissionLatch(context.Background(), red)
	if err != nil {
		t.Fatal(err)
	}
	if !latch.Latched || latch.RedSamples != 2 || latch.LatchedAt == nil {
		t.Fatalf("sustained red did not latch: %+v", latch)
	}

	// Release needs the configured number of spaced recovery samples.
	recovered := WorkAdmissionObservation{Recovered: true, Dimension: "cpu"}
	coordinator.Now = func() time.Time { return now.Add(4 * workAdmissionLatchSampleInterval) }
	latch, err = coordinator.ObserveAdmissionLatch(context.Background(), recovered)
	if err != nil {
		t.Fatal(err)
	}
	if !latch.Latched || latch.RecoverySamples != 1 {
		t.Fatalf("released on a single recovery sample: %+v", latch)
	}
	coordinator.Now = func() time.Time { return now.Add(6 * workAdmissionLatchSampleInterval) }
	latch, err = coordinator.ObserveAdmissionLatch(context.Background(), recovered)
	if err != nil {
		t.Fatal(err)
	}
	if latch.Latched || latch.RecoverySamples != 0 {
		t.Fatalf("sustained recovery did not release: %+v", latch)
	}
}

// TestNonCPUPressureDoesNotDisturbTheSharedCPULatch keeps the dimensions
// independent. The shared latch is host-wide, so folding a memory- or
// storage-pressure poll into it cleared the recovery counter for everyone: one
// memory-blocked process would repeatedly wipe every other waiter's CPU-recovery
// progress and the CPU latch could never release.
func TestNonCPUPressureDoesNotDisturbTheSharedCPULatch(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	now := time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC)
	coordinator.Now = func() time.Time { return now }
	gate := &workAdmissionGate{
		limits: testWorkLimits(),
		sharedLatch: func(observation WorkAdmissionObservation) (WorkAdmissionLatch, error) {
			return coordinator.ObserveAdmissionLatch(context.Background(), observation)
		},
	}

	// Establish CPU recovery progress from a CPU-dimension waiter.
	red := WorkAdmissionObservation{Red: true, Dimension: "cpu"}
	if _, err := coordinator.ObserveAdmissionLatch(context.Background(), red); err != nil {
		t.Fatal(err)
	}
	coordinator.Now = func() time.Time { return now.Add(2 * workAdmissionLatchSampleInterval) }
	if _, err := coordinator.ObserveAdmissionLatch(context.Background(), red); err != nil {
		t.Fatal(err)
	}
	coordinator.Now = func() time.Time { return now.Add(4 * workAdmissionLatchSampleInterval) }
	progressed, err := coordinator.ObserveAdmissionLatch(context.Background(), WorkAdmissionObservation{Recovered: true, Dimension: "cpu"})
	if err != nil || progressed.RecoverySamples != 1 {
		t.Fatalf("recovery progress not established: %+v err=%v", progressed, err)
	}

	// A memory-pressure poll from an unrelated waiter must not disturb it.
	coordinator.Now = func() time.Time { return now.Add(6 * workAdmissionLatchSampleInterval) }
	gate.Observe(Admission{
		Allowed: false, Level: LevelRed,
		Reasons: []string{"host free memory 10% <= red 15%"},
	}, false)

	after, err := coordinator.ObserveAdmissionLatch(context.Background(), WorkAdmissionObservation{Recovered: true, Dimension: "cpu"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Latched || after.RecoverySamples != 0 {
		t.Fatalf("memory pressure erased CPU recovery progress: %+v", after)
	}
}

// TestSharedAdmissionLatchExpiresWhenUnobserved stops a latch outliving every
// process that established it.
func TestSharedAdmissionLatchExpiresWhenUnobserved(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	now := time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC)
	coordinator.Now = func() time.Time { return now }
	red := WorkAdmissionObservation{Red: true, Dimension: "cpu"}
	if _, err := coordinator.ObserveAdmissionLatch(context.Background(), red); err != nil {
		t.Fatal(err)
	}
	coordinator.Now = func() time.Time { return now.Add(2 * workAdmissionLatchSampleInterval) }
	latch, err := coordinator.ObserveAdmissionLatch(context.Background(), red)
	if err != nil || !latch.Latched {
		t.Fatalf("latch=%+v err=%v", latch, err)
	}
	coordinator.Now = func() time.Time { return now.Add(2*workAdmissionLatchSampleInterval + 2*workAdmissionLatchStale) }
	latch, err = coordinator.ObserveAdmissionLatch(context.Background(), WorkAdmissionObservation{Recovered: true})
	if err != nil {
		t.Fatal(err)
	}
	if latch.Latched {
		t.Fatalf("stale latch was inherited: %+v", latch)
	}
}

// TestRecentSchemaDoesNotBlockAcquisitionBehindALiveLease is the regression for a
// live outage: a single long-running lease written by an n-1 helper held the
// state-upgrade barrier, and the barrier refused *every* acquisition on the host
// for that lease's whole lifetime — 43 minutes observed, with five of eight
// weighted slots free and an empty queue. From schema 6 up the differences are
// additive, so the mutation proceeds and the document simply stays where it is.
func TestRecentSchemaDoesNotBlockAcquisitionBehindALiveLease(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	live := fmt.Sprintf(`{
  "schema_version": %d,
  "leases": [{
    "id": "00000000000000000000000000000001",
    "operation_id": "00000000000000000000000000000011",
    "class": "test",
    "weight": 3,
    "pid": 100,
    "owner_identity": "test:100",
    "supervisor_pid": 100,
    "supervisor_identity": "test:100",
    "queued_at": %q,
    "started_at": %q
  }],
  "waiters": []
}
`, workReservationMinimumSchema, coordinator.Now().UTC().Format(time.RFC3339Nano), coordinator.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(coordinator.statePath(), []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}
	lease, status, err := coordinator.AcquireOperation(context.Background(), WorkClassExpressTest, "00000000000000000000000000000022")
	if err != nil {
		t.Fatalf("acquisition blocked behind a live lease on a representable schema: %v", err)
	}
	if status.SchemaVersion != workReservationMinimumSchema {
		t.Fatalf("document should stay pinned while the legacy lease lives: schema=%d", status.SchemaVersion)
	}
	if len(status.Leases) != 2 {
		t.Fatalf("leases=%d want 2", len(status.Leases))
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestUnrepresentableSchemaStillFailsClosed keeps the barrier where it is
// load-bearing: schemas below 6 carry no operation_id and no supervisor identity,
// so writing a lease into them would lose ownership attribution outright.
func TestUnrepresentableSchemaStillFailsClosed(t *testing.T) {
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
	if _, _, err := coordinator.AcquireOperation(context.Background(), WorkClassTest, "00000000000000000000000000000033"); !errors.Is(err, ErrWorkUpgradePending) {
		t.Fatalf("schema 2 acquisition must still fail closed: %v", err)
	}
}

// TestAdmissionHoldSurvivesSchemaSixDowngrade proves the n-1 writer keeps older
// helpers readable while the new observability fields are simply absent.
func TestAdmissionHoldRoundTripsAndDowngradesToSchemaSix(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	if err := coordinator.HoldAdmission(context.Background(), holdTestOperationB, WorkClassBrowser, WorkAdmissionObservation{Red: true, Dimension: "cpu"}); err != nil {
		t.Fatal(err)
	}
	state, err := coordinator.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != workStateSchemaVersion || len(state.AdmissionHolds) != 1 {
		t.Fatalf("state=%+v", state)
	}
	state.SchemaVersion = 6
	if err := coordinator.writeState(state); err != nil {
		t.Fatal(err)
	}
	downgraded, err := coordinator.readState()
	if err != nil {
		t.Fatalf("schema-6 document must stay readable: %v", err)
	}
	if downgraded.SchemaVersion != 6 {
		t.Fatalf("schema=%d", downgraded.SchemaVersion)
	}
	if len(downgraded.AdmissionHolds) != 0 {
		t.Fatalf("schema-6 document must not carry admission holds: %+v", downgraded.AdmissionHolds)
	}
}
