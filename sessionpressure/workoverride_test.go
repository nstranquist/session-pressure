package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestWorkOverrideRunsSelectedWaiterNextWithoutBypassingCapacity(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 16, 20, 0, 0, 0, time.UTC)
	alive := func(pid int) bool { return pid == 100 || pid == 200 || pid == 300 || pid == 400 || pid == 999 }
	identity := func(pid int) (string, error) { return fmt.Sprintf("owner:%d", pid), nil }
	makeCoordinator := func(pid int) *WorkCoordinator {
		return &WorkCoordinator{
			Dir: dir, Limits: testWorkLimits(), PID: pid, Now: func() time.Time { return now },
			ProcessAlive: alive, ProcessIdentity: identity, EventStore: NewWorkEventStore(dir),
			SchedulingPolicy: WorkSchedulingPolicy,
		}
	}

	active, _, err := makeCoordinator(300).AcquireOperation(context.Background(), WorkClassHeavy, "00000000000000000000000000000030")
	if err != nil {
		t.Fatal(err)
	}
	build, _, err := makeCoordinator(100).RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	browser, _, err := makeCoordinator(200).RegisterWaiter(context.Background(), WorkClassBrowser, "00000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := makeCoordinator(400).RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000003")
	if err != nil {
		t.Fatal(err)
	}

	before := mustWorkStatus(t, makeCoordinator(999))
	if before.SelectedOperationID != browser.operationID {
		t.Fatalf("ordinary selector=%+v, want feasible browser", before)
	}
	result, overridden, err := makeCoordinator(999).OverrideWaiter(context.Background(), target.operationID)
	if err != nil {
		t.Fatal(err)
	}
	if result.PreviousPosition != 3 || result.AlreadyRequested || overridden.OverrideOperationID != target.operationID || overridden.DecisionReason != "priority_override_bounded_drain" {
		t.Fatalf("override result=%+v status=%+v", result, overridden)
	}
	if overridden.SelectedOperationID != "" || overridden.ProtectedOperationID != target.operationID {
		t.Fatalf("override bypassed capacity instead of draining: %+v", overridden)
	}
	if !overridden.Waiters[2].Protected || overridden.Waiters[2].ProtectionReason != "priority_override" {
		t.Fatalf("overridden waiter not projected: %+v", overridden.Waiters[2])
	}
	second, _, err := makeCoordinator(999).OverrideWaiter(context.Background(), target.operationID)
	if err != nil || !second.AlreadyRequested {
		t.Fatalf("idempotent override=%+v err=%v", second, err)
	}
	if lease, status, acquireErr := browser.TryAcquire(context.Background()); lease != nil || !errors.Is(acquireErr, ErrWorkReservation) || status.ProtectedOperationID != target.operationID {
		t.Fatalf("younger waiter bypassed override: lease=%+v status=%+v err=%v", lease, status, acquireErr)
	}
	if err := active.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := target.TryAcquire(context.Background())
	if err != nil || lease == nil || acquired.SelectedOperationID != target.operationID || acquired.DecisionReason != "priority_override_selected" {
		t.Fatalf("overridden target did not acquire next: lease=%+v status=%+v err=%v", lease, acquired, err)
	}
	if acquired.OverrideOperationID != "" || acquired.OverrideRequestedAt != nil {
		t.Fatalf("one-shot override remained after acquisition: %+v", acquired)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = build.Cancel(context.Background())
	_ = browser.Cancel(context.Background())

	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{Event: WorkEventOverrideRequested})
	if err != nil || len(events) != 1 {
		t.Fatalf("override audit events=%+v err=%v", events, err)
	}
	if events[0].OperationID != target.operationID || events[0].QueuePosition != 3 || events[0].DecisionReason != "operator_override_requested" {
		t.Fatalf("override audit event=%+v", events[0])
	}
}

func TestWorkOverrideRejectsStaleOperationAndCancelClearsIntent(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	if _, _, err := coordinator.OverrideWaiter(context.Background(), "00000000000000000000000000000001"); !errors.Is(err, ErrWorkWaiterNotFound) {
		t.Fatalf("missing override err=%v", err)
	}
	waiter, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.OverrideWaiter(context.Background(), waiter.operationID); err != nil {
		t.Fatal(err)
	}
	if err := waiter.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := mustWorkStatus(t, coordinator)
	if status.OverrideOperationID != "" || status.QueueDepth != 0 {
		t.Fatalf("cancel retained override: %+v", status)
	}
}

// TestWorkOverrideAllPinsQueueOrderAndAdvancesHead proves the operator sequence
// is durable control-plane state: one confirmed request pins the whole queue,
// and the reservation moves to the next pinned waiter as each one acquires
// rather than evaporating after the first.
func TestWorkOverrideAllPinsQueueOrderAndAdvancesHead(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	first, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}
	third, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBrowser, "00000000000000000000000000000003")
	if err != nil {
		t.Fatal(err)
	}

	results, status, err := coordinator.OverrideAllWaiters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("pinned=%d, want the whole queue: %+v", len(results), results)
	}
	for index, result := range results {
		if result.OverridePosition != index+1 {
			t.Fatalf("override position=%d at index %d: %+v", result.OverridePosition, index, results)
		}
	}
	if status.OverrideOperationID != first.operationID {
		t.Fatalf("head=%q, want the queue head %q", status.OverrideOperationID, first.operationID)
	}
	if len(status.OverrideQueue) != 2 || status.OverrideQueue[0] != second.operationID || status.OverrideQueue[1] != third.operationID {
		t.Fatalf("pending tail=%+v", status.OverrideQueue)
	}
	if status.OverrideQueueDepth != 3 {
		t.Fatalf("override queue depth=%d, want the head plus its tail", status.OverrideQueueDepth)
	}
	if status.Waiters[0].ProtectionReason != "priority_override" || status.Waiters[1].ProtectionReason != "priority_override_queued" {
		t.Fatalf("pinned waiters not projected: %+v", status.Waiters)
	}
	if status.Waiters[2].OverridePosition != 3 {
		t.Fatalf("tail waiter override position=%d: %+v", status.Waiters[2].OverridePosition, status.Waiters[2])
	}

	// Only the head may admit while the sequence is pinned; the reservation then
	// moves down the list instead of releasing the queue back to ordinary policy.
	if lease, blocked, acquireErr := third.TryAcquire(context.Background()); lease != nil || !errors.Is(acquireErr, ErrWorkReservation) || blocked.ProtectedOperationID != first.operationID {
		t.Fatalf("pinned tail bypassed its own head: lease=%+v status=%+v err=%v", lease, blocked, acquireErr)
	}
	lease, advanced, err := first.TryAcquire(context.Background())
	if err != nil || lease == nil {
		t.Fatalf("pinned head did not acquire: lease=%+v err=%v", lease, err)
	}
	if advanced.OverrideOperationID != second.operationID {
		t.Fatalf("head did not advance to the next pinned waiter: %+v", advanced)
	}
	if len(advanced.OverrideQueue) != 1 || advanced.OverrideQueue[0] != third.operationID {
		t.Fatalf("advanced tail=%+v", advanced.OverrideQueue)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A pinned entry that leaves the queue without acquiring is dropped, and the
	// survivor behind it inherits the head slot.
	if err := second.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	final := mustWorkStatus(t, coordinator)
	if final.OverrideOperationID != third.operationID || len(final.OverrideQueue) != 0 || final.OverrideQueueDepth != 1 {
		t.Fatalf("cancelled pin did not hand off: %+v", final)
	}
	if err := third.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	drained := mustWorkStatus(t, coordinator)
	if drained.OverrideOperationID != "" || drained.OverrideRequestedAt != nil || len(drained.OverrideQueue) != 0 || drained.OverrideQueueDepth != 0 {
		t.Fatalf("drained sequence retained operator intent: %+v", drained)
	}

	events, err := NewWorkEventStore(coordinator.Dir).Read(WorkEventFilter{Event: WorkEventOverrideRequested})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("override audit events=%d, want one per pinned operation", len(events))
	}
}

// TestWorkOverrideReplacesEarlierSequence keeps the documented contract that the
// most recent confirmed request is the whole operator intent, not a union with
// whatever was pinned before it.
func TestWorkOverrideReplacesEarlierSequence(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	first, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.OverrideAllWaiters(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.OverrideWaiter(context.Background(), second.operationID); err != nil {
		t.Fatal(err)
	}
	status := mustWorkStatus(t, coordinator)
	if status.OverrideOperationID != second.operationID || len(status.OverrideQueue) != 0 || status.OverrideQueueDepth != 1 {
		t.Fatalf("single override did not replace the sequence: %+v", status)
	}
	if status.Waiters[0].OverridePosition != 0 || status.Waiters[0].ProtectionReason == "priority_override_queued" {
		t.Fatalf("unpinned waiter still projected as pinned: %+v", status.Waiters[0])
	}

	// Re-confirming the identical sequence is idempotent and writes no new audit.
	repeat, _, err := coordinator.OverrideWaiters(context.Background(), []string{second.operationID})
	if err != nil || len(repeat) != 1 || !repeat[0].AlreadyRequested {
		t.Fatalf("idempotent re-confirm=%+v err=%v", repeat, err)
	}
	events, err := NewWorkEventStore(coordinator.Dir).Read(WorkEventFilter{Event: WorkEventOverrideRequested})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("override audit events=%d, want 2 from --all plus 1 promotion", len(events))
	}
	_ = first.Cancel(context.Background())
	_ = second.Cancel(context.Background())
}

func TestAgentPriorityAppendsBehindOperatorSequence(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	operator, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}
	ordinary, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBrowser, "00000000000000000000000000000003")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.OverrideWaiter(context.Background(), operator.operationID); err != nil {
		t.Fatal(err)
	}
	result, status, err := coordinator.PrioritizeWaiter(context.Background(), agent.operationID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Actor != "agent" || result.OperationID != agent.operationID || result.OverridePosition != 2 {
		t.Fatalf("agent priority result=%+v", result)
	}
	if status.OverrideOperationID != operator.operationID || len(status.OverrideQueue) != 1 || status.OverrideQueue[0] != agent.operationID {
		t.Fatalf("agent displaced operator order: %+v", status)
	}
	if status.Waiters[2].OverridePosition != 0 {
		t.Fatalf("ordinary waiter unexpectedly pinned: %+v", status.Waiters)
	}

	events, err := NewWorkEventStore(coordinator.Dir).Read(WorkEventFilter{Event: WorkEventOverrideRequested})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("priority audit events=%+v", events)
	}
	var agentEvent WorkEvent
	for _, event := range events {
		if event.Outcome == "agent_priority_requested" {
			agentEvent = event
		}
	}
	if agentEvent.OperationID != agent.operationID || agentEvent.DecisionReason != "agent_priority_appended" {
		t.Fatalf("agent priority audit event=%+v events=%+v", agentEvent, events)
	}
	stats := SummarizeWorkEvents(events, time.Time{}, time.Now())
	if stats.ReviewSignals.OperatorOverrides != 1 || stats.ReviewSignals.AgentPriorityRequests != 1 {
		t.Fatalf("priority review signals=%+v", stats.ReviewSignals)
	}
	_ = operator.Cancel(context.Background())
	_ = agent.Cancel(context.Background())
	_ = ordinary.Cancel(context.Background())
}

func TestWorkOverrideAllRejectsEmptyQueueAndUnknownOperation(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	if _, _, err := coordinator.OverrideAllWaiters(context.Background()); !errors.Is(err, ErrWorkWaiterNotFound) {
		t.Fatalf("empty-queue override err=%v", err)
	}
	waiter, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	// A sequence is all-or-nothing: one unknown identity must not silently pin
	// the recognized subset.
	if _, _, err := coordinator.OverrideWaiters(context.Background(), []string{waiter.operationID, "00000000000000000000000000000009"}); !errors.Is(err, ErrWorkWaiterNotFound) {
		t.Fatalf("partial sequence err=%v", err)
	}
	status := mustWorkStatus(t, coordinator)
	if status.OverrideOperationID != "" || status.OverrideQueueDepth != 0 {
		t.Fatalf("rejected sequence left partial intent: %+v", status)
	}
	if _, _, err := coordinator.OverrideWaiters(context.Background(), nil); err == nil {
		t.Fatal("empty operation list must be rejected")
	}
	_ = waiter.Cancel(context.Background())
}

// TestWorkOverrideQueueDowngradesToSchemaSeven proves an n-1 helper keeps the
// active head — the safety-relevant half — and merely loses the pending tail.
func TestWorkOverrideQueueDowngradesToSchemaSeven(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	if _, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000001"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000002"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.OverrideAllWaiters(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := coordinator.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != workStateSchemaVersion || len(state.OverrideQueue) != 1 {
		t.Fatalf("state=%+v", state)
	}
	state.SchemaVersion = 7
	if err := coordinator.writeState(state); err != nil {
		t.Fatal(err)
	}
	downgraded, err := coordinator.readState()
	if err != nil {
		t.Fatalf("schema-7 document must stay readable: %v", err)
	}
	if downgraded.SchemaVersion != 7 {
		t.Fatalf("schema=%d", downgraded.SchemaVersion)
	}
	if downgraded.OverrideOperationID != "00000000000000000000000000000001" {
		t.Fatalf("schema-7 downgrade dropped the active head: %+v", downgraded)
	}
	if len(downgraded.OverrideQueue) != 0 {
		t.Fatalf("schema-7 document must not carry the pending tail: %+v", downgraded.OverrideQueue)
	}
	if len(downgraded.Waiters) != 2 {
		t.Fatalf("schema-7 downgrade lost queue records: %+v", downgraded.Waiters)
	}
}

// TestWorkOverrideSequenceFailsClosedOnSingleSlotState is the honesty gate for
// --all. A mutation is accepted from schema 6 up while the document stays pinned
// at its own schema, and those downgrade writers carry only the override head —
// so persisting one entry while reporting N pinned is the failure to prevent.
func TestWorkOverrideSequenceFailsClosedOnSingleSlotState(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	first, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}

	// Pin the persisted document to the pre-queue shape the way a live legacy
	// cohort does.
	state, err := coordinator.readState()
	if err != nil {
		t.Fatal(err)
	}
	state.SchemaVersion = workOverrideQueueMinimumSchema - 1
	if err := coordinator.writeState(state); err != nil {
		t.Fatal(err)
	}

	if _, _, err := coordinator.OverrideAllWaiters(context.Background()); !errors.Is(err, ErrWorkOverrideQueueUnsupported) {
		t.Fatalf("multi-operation pin on a single-slot document err=%v", err)
	}
	blocked := mustWorkStatus(t, coordinator)
	if blocked.OverrideOperationID != "" || blocked.OverrideQueueDepth != 0 {
		t.Fatalf("refused sequence still recorded intent: %+v", blocked)
	}

	// A single promotion is exactly what the old shape can carry, so it stays
	// available rather than being blocked alongside the sequence.
	if _, _, err := coordinator.OverrideWaiters(context.Background(), []string{second.operationID}); err != nil {
		t.Fatalf("single override on a single-slot document: %v", err)
	}
	single := mustWorkStatus(t, coordinator)
	if single.OverrideOperationID != second.operationID || single.OverrideQueueDepth != 1 {
		t.Fatalf("single override status=%+v", single)
	}
	_ = first.Cancel(context.Background())
	_ = second.Cancel(context.Background())
}

// TestWorkOverrideClearReleasesSequence proves a bulk promotion is reversible:
// without a release the only way out of a mistaken --all was to drain it.
func TestWorkOverrideClearReleasesSequence(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	if _, _, err := coordinator.ClearWaiterOverride(context.Background()); !errors.Is(err, ErrWorkOverrideNotPinned) {
		t.Fatalf("clearing an unpinned queue err=%v", err)
	}
	first, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.OverrideAllWaiters(context.Background()); err != nil {
		t.Fatal(err)
	}

	cleared, status, err := coordinator.ClearWaiterOverride(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared) != 2 || cleared[0].OperationID != first.operationID || cleared[1].OperationID != second.operationID {
		t.Fatalf("clear receipts=%+v, want head-first order", cleared)
	}
	if status.OverrideOperationID != "" || len(status.OverrideQueue) != 0 || status.OverrideQueueDepth != 0 {
		t.Fatalf("clear left operator intent behind: %+v", status)
	}
	for _, waiter := range status.Waiters {
		if waiter.OverridePosition != 0 || waiter.ProtectionReason == "priority_override" || waiter.ProtectionReason == "priority_override_queued" {
			t.Fatalf("released waiter still projected as pinned: %+v", waiter)
		}
	}
	// The released queue must admit again under ordinary policy.
	if status.DecisionReason == "priority_override_bounded_drain" {
		t.Fatalf("selector still reports an override decision: %+v", status)
	}

	events, err := NewWorkEventStore(coordinator.Dir).Read(WorkEventFilter{Event: WorkEventOverrideRequested})
	if err != nil {
		t.Fatal(err)
	}
	releases := 0
	for _, event := range events {
		if event.Outcome == "operator_priority_override_cleared" {
			releases++
			if event.DecisionReason != "operator_override_cleared" || event.SelectedOperationID != "" {
				t.Fatalf("release audit event=%+v", event)
			}
		}
	}
	if releases != 2 {
		t.Fatalf("release audit rows=%d, want one per released operation", releases)
	}
	_ = first.Cancel(context.Background())
	_ = second.Cancel(context.Background())
}

func TestWorkOverrideAuditsEachDistinctSelection(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	// Hold the wall clock fixed: request identity, not timestamp resolution,
	// must preserve every distinct A -> B -> A operator decision.
	coordinator.Now = func() time.Time {
		return time.Date(2026, 7, 16, 21, 0, 0, 0, time.UTC)
	}
	first, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassTest, "00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := coordinator.RegisterWaiter(context.Background(), WorkClassBuild, "00000000000000000000000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.OverrideWaiter(context.Background(), first.operationID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.OverrideWaiter(context.Background(), second.operationID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.OverrideWaiter(context.Background(), first.operationID); err != nil {
		t.Fatal(err)
	}

	events, err := NewWorkEventStore(coordinator.Dir).Read(WorkEventFilter{Event: WorkEventOverrideRequested})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("override audit events=%+v, want 3", events)
	}
	for index := 1; index < len(events); index++ {
		if events[index-1].EventID == events[index].EventID {
			t.Fatalf("distinct overrides reused event identity: %+v", events)
		}
	}
	seenRequests := map[string]struct{}{}
	for _, event := range events {
		if !validPrivateID(event.RequestID) {
			t.Fatalf("override request identity=%q event=%+v", event.RequestID, event)
		}
		if _, duplicate := seenRequests[event.RequestID]; duplicate {
			t.Fatalf("distinct overrides reused request identity: %+v", events)
		}
		seenRequests[event.RequestID] = struct{}{}
	}
}
