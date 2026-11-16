package sessionpressure

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSelectorDemotesAWaiterSelectedTooLongWithoutStarting(t *testing.T) {
	now := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC)
	stale := now.Add(-workSelectedStartBound - time.Second)
	fresh := now.Add(-5 * time.Second)
	reserved := now.Add(-time.Minute)
	head := WorkWaiterRecord{OperationID: "head", Class: WorkClassHeavy, Weight: 5, QueuedAt: reserved,
		ReservationKind: WorkReservationPressure, ReservedAt: &reserved, ResumedAt: &reserved}
	next := WorkWaiterRecord{OperationID: "next", Class: WorkClassTest, Weight: 2, QueuedAt: now}

	for _, selector := range map[string]func([]WorkWaiterRecord) workSelection{
		"bounded-lookahead": func(w []WorkWaiterRecord) workSelection {
			return selectWorkWaiter(w, 0, 8, now, workGreenExpressWindow{}, 0)
		},
		"fifo": func(w []WorkWaiterRecord) workSelection {
			return selectFIFOWorkWaiter(w, 0, 8, now, workGreenExpressWindow{})
		},
	} {
		// Within the bound the protected head holds its selection.
		head.SelectedAt = &fresh
		if got := selector([]WorkWaiterRecord{head, next}); got.SelectedOperationID != "head" {
			t.Fatalf("fresh head lost its turn: %+v", got)
		}
		// Past the bound the queue moves on, without charging a bypass.
		head.SelectedAt = &stale
		got := selector([]WorkWaiterRecord{head, next})
		if got.SelectedOperationID != "next" || len(got.BypassedIndexes) != 0 || got.ProtectedOperationID == "head" {
			t.Fatalf("stuck head still blocks: %+v", got)
		}
		// Alone, the demoted waiter can still start.
		if got := selector([]WorkWaiterRecord{head}); got.SelectedOperationID != "head" {
			t.Fatalf("demoted waiter can never start: %+v", got)
		}
	}
}

func TestNoteSelectionStartRules(t *testing.T) {
	now := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC)
	old := now.Add(-time.Minute)
	recent := now.Add(-5 * time.Second)
	waiters := []WorkWaiterRecord{{OperationID: "a"}, {OperationID: "b", SelectedAt: &recent}, {OperationID: "c", SelectedAt: &old}, {OperationID: "owner", SelectedAt: &recent}}
	// A successor selected past the head is never stamped.
	noteSelectionStart(waiters, workSelection{SelectedOperationID: "b"}, "owner", now)
	if waiters[0].SelectedAt != nil || waiters[1].SelectedAt != nil {
		t.Fatal("a pass-over selection stamped a waiter")
	}
	noteSelectionStart(waiters, workSelection{SelectedOperationID: "a"}, "owner", now)
	if waiters[0].SelectedAt == nil || !waiters[0].SelectedAt.Equal(now) {
		t.Fatal("selected waiter not stamped")
	}
	if waiters[1].SelectedAt != nil {
		t.Fatal("unselected waiter within the bound kept its stamp")
	}
	if waiters[2].SelectedAt == nil {
		t.Fatal("expired waiter lost its stamp")
	}
	if waiters[3].SelectedAt == nil {
		t.Fatal("the owner's own view cleared its stamp")
	}
	// A repeat selection keeps the first stamp.
	noteSelectionStart(waiters, workSelection{SelectedOperationID: "a"}, "owner", now.Add(time.Second))
	if !waiters[0].SelectedAt.Equal(now) {
		t.Fatal("stamp slid")
	}
}

// Reproduces 2026-11-15: the head's owner runs with an older, derated
// capacity and never sees itself fit, while every other process sees it
// selected and waits.
func TestStaleOwnerHeadNoLongerBlocksTheQueue(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	limits := testWorkLimits()
	limits.SchedulingPolicy = WorkSchedulingPolicy
	coordinator.Limits = limits
	now := coordinator.Now()
	coordinator.Now = func() time.Time { return now }
	ctx := context.Background()
	head, _, err := coordinator.RegisterWaiter(ctx, WorkClassEmulator, "00000000000000000000000000000011") // weight 5
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := coordinator.RegisterWaiter(ctx, WorkClassTest, "00000000000000000000000000000012") // weight 3
	if err != nil {
		t.Fatal(err)
	}
	tick := func() (headLease, nextLease *WorkLease, nextErr error) {
		// The stale owner: derated to 4, so a weight-5 head never fits.
		if lease, _, err := head.TryAcquireWithCapacity(ctx, 4); err == nil {
			headLease = lease
		}
		nextLease, _, nextErr = next.TryAcquireWithCapacity(ctx, 8)
		return
	}
	for elapsed := time.Duration(0); elapsed < workSelectedStartBound; elapsed += 2 * time.Second {
		headLease, nextLease, nextErr := tick()
		if headLease != nil || nextLease != nil {
			t.Fatalf("at %s: unexpected start head=%v next=%v", elapsed, headLease != nil, nextLease != nil)
		}
		if !errors.Is(nextErr, ErrWorkFairness) && !errors.Is(nextErr, ErrWorkReservation) {
			t.Fatalf("at %s: next blocked for another reason: %v", elapsed, nextErr)
		}
		now = now.Add(2 * time.Second)
	}
	status := mustWorkStatus(t, coordinator)
	if len(status.Waiters) != 2 || !status.Waiters[0].StartExpired {
		t.Fatalf("stuck head not reported: %+v", status.Waiters)
	}
	_, nextLease, err := tick()
	if err != nil || nextLease == nil {
		t.Fatalf("queue still blocked after the bound: %v", err)
	}
	if err := nextLease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	// With a fresh view the demoted head still starts.
	lease, _, err := head.TryAcquireWithCapacity(ctx, 8)
	if err != nil {
		t.Fatalf("demoted head cannot start: %v", err)
	}
	_ = lease.Release(ctx)
}
