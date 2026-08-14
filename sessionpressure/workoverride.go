package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

var ErrWorkWaiterNotFound = errors.New("queued work operation not found")

// ErrWorkOverrideNotPinned keeps a clear request honest: nothing was pinned, so
// nothing was released. Reporting success here would imply an authority change
// that never happened.
var ErrWorkOverrideNotPinned = errors.New("no operator promotion sequence is pinned")

// ErrWorkOverrideQueueUnsupported fails a multi-operation sequence closed while
// the persisted document predates the ordered queue.
//
// A mutation is accepted from schema 6 up, but the document stays pinned at its
// own schema until the queue genuinely drains, and those downgrade writers carry
// only the single override head. Persisting one entry while reporting N pinned
// would be the worst outcome available: the operator sees a confirmed sequence
// and the coordinator honours only its first element.
var ErrWorkOverrideQueueUnsupported = errors.New("this host's work state still uses a single-slot override; pin one operation with --operation-id, or wait for the queue to drain so the state can carry an ordered sequence")

// workOverrideQueueMinimumSchema is the first persisted shape with an ordered
// override tail. Below it, only a single head survives a write.
const workOverrideQueueMinimumSchema = 8

// persistedWorkStateSchema is the schema this state will actually be written
// at, mirroring withState's pinning rule. Callers must plan against what will
// survive the write, not what the in-memory struct can hold.
func persistedWorkStateSchema(state *workState) int {
	if state == nil {
		return workStateSchemaVersion
	}
	legacyPinned := state.legacyActive && (len(state.Leases) > 0 || len(state.Waiters) > 0)
	if !legacyPinned && state.SchemaVersion < workStateSchemaVersion {
		return workStateSchemaVersion
	}
	return state.SchemaVersion
}

// WorkMaximumOverrideQueue bounds one confirmed promotion sequence. The queue
// is operator intent over a live snapshot, so it never needs to exceed the
// selector's own scan horizon.
const WorkMaximumOverrideQueue = workSelectorScanLimit

// WorkOverrideResult is the privacy-bounded receipt for one explicit operator
// priority override. It identifies the existing queue record, never its
// command, arguments, environment, or working directory.
type WorkOverrideResult struct {
	OperationID      string    `json:"operation_id"`
	Class            WorkClass `json:"class"`
	Weight           int       `json:"weight"`
	PID              int       `json:"pid"`
	Actor            string    `json:"actor,omitempty"`
	PreviousPosition int       `json:"previous_position"`
	RequestedAt      time.Time `json:"requested_at"`
	AlreadyRequested bool      `json:"already_requested"`
	// OverridePosition is the 1-based place this operation holds in the pinned
	// sequence. Position 1 is the active head; the rest wait their turn.
	OverridePosition int `json:"override_position"`
}

// selectStateWaiter gives one explicit operator override precedence over the
// ordinary FIFO/bounded-lookahead policy. It changes queue order only: the
// hard weighted-capacity ceiling and the separate host-pressure admission gate
// remain authoritative.
func (coordinator *WorkCoordinator) selectStateWaiter(state workState, used, capacity int, now time.Time, green workGreenExpressWindow) workSelection {
	if state.OverrideOperationID != "" {
		if selection, found := selectOverriddenWorkWaiter(state.Waiters, used, capacity, state.OverrideOperationID); found {
			return selection
		}
	}
	return coordinator.selectWaiter(state.Waiters, used, capacity, now, green, expressLeasedWeight(state.Leases))
}

func selectOverriddenWorkWaiter(waiters []WorkWaiterRecord, used, capacity int, operationID string) (workSelection, bool) {
	for _, waiter := range waiters {
		if waiter.OperationID != operationID {
			continue
		}
		selection := workSelection{
			ProtectedOperationID: operationID,
			DecisionReason:       "priority_override_bounded_drain",
		}
		if waiter.Weight <= max(0, capacity-used) {
			selection.SelectedOperationID = operationID
			selection.DecisionReason = "priority_override_selected"
		}
		return selection, true
	}
	return workSelection{}, false
}

// workOverridePositions maps every still-queued pinned operation to its 1-based
// place in the sequence, head first. Entries whose waiter has left the queue are
// absent, so callers never report a promotion that can no longer happen.
func workOverridePositions(state *workState) map[string]int {
	if state == nil {
		return map[string]int{}
	}
	live := make(map[string]struct{}, len(state.Waiters))
	for _, waiter := range state.Waiters {
		live[waiter.OperationID] = struct{}{}
	}
	positions := make(map[string]int, len(state.OverrideQueue)+1)
	next := 0
	for _, operationID := range append([]string{state.OverrideOperationID}, state.OverrideQueue...) {
		if operationID == "" {
			continue
		}
		if _, queued := live[operationID]; !queued {
			continue
		}
		if _, duplicate := positions[operationID]; duplicate {
			continue
		}
		next++
		positions[operationID] = next
	}
	return positions
}

// advanceWorkOverride keeps the operator promotion sequence honest against the
// live queue: dead or acquired entries are dropped, and the next surviving
// entry inherits the head reservation. It replaces the earlier single-slot
// clear, which discarded the whole request the moment the head left the queue.
func advanceWorkOverride(state *workState, now time.Time) bool {
	if state == nil || (state.OverrideOperationID == "" && len(state.OverrideQueue) == 0) {
		return false
	}
	live := make(map[string]struct{}, len(state.Waiters))
	for _, waiter := range state.Waiters {
		live[waiter.OperationID] = struct{}{}
	}

	ordered := make([]string, 0, len(state.OverrideQueue)+1)
	seen := make(map[string]struct{}, len(state.OverrideQueue)+1)
	for _, operationID := range append([]string{state.OverrideOperationID}, state.OverrideQueue...) {
		if operationID == "" {
			continue
		}
		if _, duplicate := seen[operationID]; duplicate {
			continue
		}
		if _, queued := live[operationID]; !queued {
			continue
		}
		seen[operationID] = struct{}{}
		ordered = append(ordered, operationID)
	}

	head := ""
	tail := []string{}
	if len(ordered) > 0 {
		head = ordered[0]
		tail = ordered[1:]
	}
	changed := head != state.OverrideOperationID || !equalStringSlices(tail, state.OverrideQueue)
	if !changed {
		return false
	}
	if head == "" {
		state.OverrideOperationID = ""
		state.OverrideRequestedAt = nil
		state.OverrideQueue = nil
		return true
	}
	if head != state.OverrideOperationID {
		// A newly promoted head starts its own reservation clock so age-based
		// review reads the promotion, not the original batch request.
		promotedAt := now
		state.OverrideOperationID = head
		state.OverrideRequestedAt = &promotedAt
	}
	if len(tail) == 0 {
		state.OverrideQueue = nil
	} else {
		state.OverrideQueue = tail
	}
	return true
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// OverrideWaiter records and applies a one-shot operator priority override.
// The selected waiter's own process remains responsible for acquiring the
// lease and running its already-private command after normal host admission.
func (coordinator *WorkCoordinator) OverrideWaiter(ctx context.Context, operationID string) (WorkOverrideResult, WorkStatus, error) {
	results, status, err := coordinator.OverrideWaiters(ctx, []string{operationID})
	if err != nil {
		return WorkOverrideResult{}, status, err
	}
	if len(results) == 0 {
		return WorkOverrideResult{}, status, fmt.Errorf("%w: operation=%s", ErrWorkWaiterNotFound, operationID)
	}
	return results[0], status, nil
}

// ClearWaiterOverride drops the whole operator promotion sequence and hands the
// queue back to ordinary policy. Without it a pinned sequence could only end by
// draining, so one mistaken bulk promotion was unrecoverable. Clearing never
// touches leases: work already admitted keeps running.
func (coordinator *WorkCoordinator) ClearWaiterOverride(ctx context.Context) ([]WorkOverrideResult, WorkStatus, error) {
	if coordinator == nil {
		return nil, WorkStatus{}, errors.New("work coordinator is required")
	}
	var cleared []WorkOverrideResult
	status, err := coordinator.withState(ctx, func(state *workState) error {
		cleared = nil
		positions := workOverridePositions(state)
		if len(positions) == 0 {
			return ErrWorkOverrideNotPinned
		}
		index := make(map[string]int, len(state.Waiters))
		for position, waiter := range state.Waiters {
			index[waiter.OperationID] = position
		}
		used := 0
		for _, lease := range state.Leases {
			used += lease.Weight
		}
		store := coordinator.EventStore
		if store == nil {
			store = NewWorkEventStore(coordinator.Dir)
		}
		limits := coordinator.limits()
		clearedAt := coordinator.now()

		ordered := make([]string, 0, len(positions))
		for operationID := range positions {
			ordered = append(ordered, operationID)
		}
		sort.Slice(ordered, func(i, j int) bool { return positions[ordered[i]] < positions[ordered[j]] })
		for _, operationID := range ordered {
			target := state.Waiters[index[operationID]]
			requestID, err := randomPrivateID()
			if err != nil {
				return fmt.Errorf("generate work override request identity: %w", err)
			}
			// Audited under the same event type: a release of operator priority is
			// part of that operation's override history, not a separate lifecycle.
			if err := store.AppendDurable(WorkEvent{
				Timestamp: clearedAt, Event: WorkEventOverrideRequested, RequestID: requestID,
				OperationID: operationID, Class: target.Class, Weight: target.Weight, PID: target.PID,
				QueuePosition: index[operationID] + 1, QueueDepth: len(state.Waiters), Capacity: limits.Capacity,
				Used: used, Available: max(0, limits.Capacity-used),
				Outcome: "operator_priority_override_cleared", SchedulingPolicy: coordinator.schedulingPolicy(),
				SelectorSchemaVersion: workSelectorSchemaVersion,
				DecisionReason:        "operator_override_cleared",
			}); err != nil {
				return fmt.Errorf("persist work override release: %w", err)
			}
			cleared = append(cleared, WorkOverrideResult{
				OperationID: operationID, Class: target.Class, Weight: target.Weight, PID: target.PID,
				PreviousPosition: index[operationID] + 1, RequestedAt: clearedAt,
				OverridePosition: positions[operationID],
			})
		}
		state.OverrideOperationID = ""
		state.OverrideRequestedAt = nil
		state.OverrideQueue = nil
		return nil
	})
	if err != nil {
		return nil, status, err
	}
	return cleared, status, nil
}

// OverrideAllWaiters pins every currently queued waiter in queue order. It
// resolves the snapshot inside the same state lock the mutation runs under, so
// a waiter that arrives or acquires mid-call can never be pinned by accident or
// silently omitted from a sequence the caller believes is complete.
func (coordinator *WorkCoordinator) OverrideAllWaiters(ctx context.Context) ([]WorkOverrideResult, WorkStatus, error) {
	return coordinator.overrideWaiters(ctx, nil, true, false, "operator")
}

// OverrideWaiters records one confirmed operator promotion sequence in the
// given order. The first entry becomes the active head and reserves the drain;
// each later entry inherits that reservation as its predecessor acquires. The
// call replaces any earlier sequence outright, so operator intent is always the
// most recent confirmed request rather than an accumulated union.
func (coordinator *WorkCoordinator) OverrideWaiters(ctx context.Context, operationIDs []string) ([]WorkOverrideResult, WorkStatus, error) {
	if len(operationIDs) == 0 {
		return nil, WorkStatus{}, errors.New("work override requires at least one operation_id")
	}
	return coordinator.overrideWaiters(ctx, operationIDs, false, false, "operator")
}

// PrioritizeWaiter appends one agent's own live waiter behind any already
// pinned operator sequence. It is deliberately queue-only: host admission,
// weighted capacity, active leases, and the operator's existing order remain
// authoritative.
func (coordinator *WorkCoordinator) PrioritizeWaiter(ctx context.Context, operationID string) (WorkOverrideResult, WorkStatus, error) {
	results, status, err := coordinator.overrideWaiters(ctx, []string{operationID}, false, true, "agent")
	if err != nil {
		return WorkOverrideResult{}, status, err
	}
	for _, result := range results {
		if result.OperationID == operationID {
			return result, status, nil
		}
	}
	return WorkOverrideResult{}, status, fmt.Errorf("%w: operation=%s", ErrWorkWaiterNotFound, operationID)
}

func (coordinator *WorkCoordinator) overrideWaiters(ctx context.Context, operationIDs []string, all, appendPinned bool, actor string) ([]WorkOverrideResult, WorkStatus, error) {
	if coordinator == nil {
		return nil, WorkStatus{}, errors.New("work coordinator is required")
	}
	for _, operationID := range operationIDs {
		if !validPrivateID(operationID) {
			return nil, WorkStatus{}, errors.New("work operation_id must be a 32-character lowercase hex identity")
		}
	}
	if len(operationIDs) > WorkMaximumOverrideQueue {
		return nil, WorkStatus{}, fmt.Errorf("work override accepts at most %d operations in one sequence", WorkMaximumOverrideQueue)
	}

	var results []WorkOverrideResult
	status, err := coordinator.withState(ctx, func(state *workState) error {
		results = nil
		requested := operationIDs
		if all {
			requested = make([]string, 0, len(state.Waiters))
			for _, waiter := range state.Waiters {
				requested = append(requested, waiter.OperationID)
			}
			if len(requested) == 0 {
				return fmt.Errorf("%w: the work queue is empty", ErrWorkWaiterNotFound)
			}
			if len(requested) > WorkMaximumOverrideQueue {
				requested = requested[:WorkMaximumOverrideQueue]
			}
		}
		previous := workOverridePositions(state)
		if appendPinned {
			pinned := make([]string, 0, len(previous))
			for operationID := range previous {
				pinned = append(pinned, operationID)
			}
			sort.Slice(pinned, func(i, j int) bool { return previous[pinned[i]] < previous[pinned[j]] })
			requested = append(pinned, requested...)
		}

		index := make(map[string]int, len(state.Waiters))
		for position, waiter := range state.Waiters {
			index[waiter.OperationID] = position
		}
		ordered := make([]string, 0, len(requested))
		seen := make(map[string]struct{}, len(requested))
		for _, operationID := range requested {
			if _, duplicate := seen[operationID]; duplicate {
				continue
			}
			if _, queued := index[operationID]; !queued {
				return fmt.Errorf("%w: operation=%s", ErrWorkWaiterNotFound, operationID)
			}
			seen[operationID] = struct{}{}
			ordered = append(ordered, operationID)
		}
		if len(ordered) > WorkMaximumOverrideQueue {
			return fmt.Errorf("work override accepts at most %d operations in one sequence", WorkMaximumOverrideQueue)
		}

		if len(ordered) > 1 && persistedWorkStateSchema(state) < workOverrideQueueMinimumSchema {
			return ErrWorkOverrideQueueUnsupported
		}

		requestedAt := coordinator.now()
		used := 0
		for _, lease := range state.Leases {
			used += lease.Weight
		}
		store := coordinator.EventStore
		if store == nil {
			store = NewWorkEventStore(coordinator.Dir)
		}
		limits := coordinator.limits()

		for sequence, operationID := range ordered {
			target := state.Waiters[index[operationID]]
			// Re-confirming the exact same pinned position is idempotent: it
			// keeps the original request clock and writes no second audit row.
			if previous[operationID] == sequence+1 {
				recordedAt := requestedAt
				if sequence == 0 && state.OverrideRequestedAt != nil {
					recordedAt = *state.OverrideRequestedAt
				}
				results = append(results, WorkOverrideResult{
					OperationID: operationID, Class: target.Class, Weight: target.Weight, PID: target.PID,
					PreviousPosition: index[operationID] + 1, RequestedAt: recordedAt,
					AlreadyRequested: true, OverridePosition: sequence + 1, Actor: actor,
				})
				continue
			}
			requestID, err := randomPrivateID()
			if err != nil {
				return fmt.Errorf("generate work override request identity: %w", err)
			}
			outcome := "operator_priority_override_requested"
			decisionReason := "operator_override_requested"
			if actor == "agent" {
				outcome = "agent_priority_requested"
				decisionReason = "agent_priority_appended"
			}
			if err := store.AppendDurable(WorkEvent{
				Timestamp: requestedAt, Event: WorkEventOverrideRequested, RequestID: requestID,
				OperationID: operationID, Class: target.Class, Weight: target.Weight, PID: target.PID,
				QueuePosition: index[operationID] + 1, QueueDepth: len(state.Waiters), Capacity: limits.Capacity,
				Used: used, Available: max(0, limits.Capacity-used),
				Outcome: outcome, SchedulingPolicy: coordinator.schedulingPolicy(),
				SelectorSchemaVersion: workSelectorSchemaVersion, SelectedOperationID: operationID,
				ProtectedOperationID: operationID, DecisionReason: decisionReason,
			}); err != nil {
				return fmt.Errorf("persist work override intent: %w", err)
			}
			results = append(results, WorkOverrideResult{
				OperationID: operationID, Class: target.Class, Weight: target.Weight, PID: target.PID,
				PreviousPosition: index[operationID] + 1, RequestedAt: requestedAt,
				OverridePosition: sequence + 1, Actor: actor,
			})
		}

		if ordered[0] != state.OverrideOperationID {
			state.OverrideOperationID = ordered[0]
			state.OverrideRequestedAt = &requestedAt
		}
		if len(ordered) > 1 {
			state.OverrideQueue = ordered[1:]
		} else {
			state.OverrideQueue = nil
		}
		return nil
	})
	if err != nil {
		return nil, status, err
	}
	return results, status, nil
}
