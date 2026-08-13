package sessionpressure

import (
	"testing"
	"time"
)

func TestIsWrapperInterruptEventClosedOutcomes(t *testing.T) {
	if IsWrapperInterruptEvent(WorkEvent{Event: WorkEventCompleted, Outcome: "wrapper_interrupt"}) {
		t.Fatal("non-cancelled must not count")
	}
	if !IsWrapperInterruptEvent(WorkEvent{Event: WorkEventCancelled, Outcome: "wrapper_interrupt"}) {
		t.Fatal("wrapper_interrupt outcome")
	}
	if !IsWrapperInterruptEvent(WorkEvent{Event: WorkEventCancelled, Outcome: "agent_kill"}) {
		t.Fatal("agent_kill")
	}
	if !IsWrapperInterruptEvent(WorkEvent{Event: WorkEventCancelled, DecisionReason: "signal_interrupt"}) {
		t.Fatal("decision_reason token")
	}
	if IsWrapperInterruptEvent(WorkEvent{Event: WorkEventCancelled, Outcome: "user_abort"}) {
		t.Fatal("unknown outcome must fail closed")
	}
	if IsWrapperInterruptEvent(WorkEvent{Event: WorkEventCancelled, Outcome: "/tmp/secret"}) {
		t.Fatal("pathy outcome must not classify")
	}
}

func TestSummarizeWorkEventsCountsWrapperInterrupts(t *testing.T) {
	now := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	events := []WorkEvent{
		{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Event: WorkEventCancelled, Class: WorkClassTest, Outcome: "wrapper_interrupt", Timestamp: now},
		{OperationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Event: WorkEventCancelled, Class: WorkClassBuild, Outcome: "user_abort", Timestamp: now},
		{OperationID: "cccccccccccccccccccccccccccccccc", Event: WorkEventCancelled, Class: WorkClassExpressTest, DecisionReason: "agent_kill", Timestamp: now},
		{OperationID: "dddddddddddddddddddddddddddddddd", Event: WorkEventCompleted, Class: WorkClassTest, Outcome: "successful", Timestamp: now},
	}
	stats := SummarizeWorkEvents(events, now.Add(-time.Hour), now)
	if stats.ReviewSignals.CancelledOperations != 3 {
		t.Fatalf("cancelled=%d", stats.ReviewSignals.CancelledOperations)
	}
	if stats.ReviewSignals.WrapperInterruptOperations != 2 {
		t.Fatalf("wrapper_interrupt_operations=%d want 2", stats.ReviewSignals.WrapperInterruptOperations)
	}
	cal := BuildWorkCalibrationReport(events, now.Add(-time.Hour), now)
	if cal.WrapperInterruptOperations != 2 {
		t.Fatalf("calibration wrapper_interrupt_operations=%d", cal.WrapperInterruptOperations)
	}
}
