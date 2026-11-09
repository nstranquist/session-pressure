package sessionpressure

import (
	"testing"
	"time"
)

func TestWorkCancellationBreakdown(t *testing.T) {
	now := time.Now().UTC()
	events := []WorkEvent{
		{OperationID: "signal", Event: WorkEventCancelled, Class: WorkClassTest, Outcome: "wrapper_interrupt", Timestamp: now},
		{OperationID: "timeout", Event: WorkEventCancelled, Class: WorkClassTest, Outcome: "wait_timeout", Timestamp: now},
		{OperationID: "legacy", Event: WorkEventCancelled, Class: WorkClassTest, Outcome: "interrupted", Timestamp: now},
	}
	// Duplicate terminal records must not inflate any category.
	events = append(events, events[0], events[1], events[2])
	stats := SummarizeWorkEvents(events, now.Add(-24*time.Hour), now)
	got := stats.ReviewSignals
	if got.SignalCancelOperations != 1 || got.WaitTimeoutOperations != 1 || got.LegacyInterruptOperations != 1 {
		t.Fatalf("breakdown: %+v", got)
	}
	if got.WrapperInterruptOperations != 2 {
		t.Fatalf("compatibility aggregate=%d want signal + legacy = 2", got.WrapperInterruptOperations)
	}
	if IsWrapperInterruptEvent(events[1]) {
		t.Fatal("wait timeout must not count as a wrapper interrupt")
	}
}
