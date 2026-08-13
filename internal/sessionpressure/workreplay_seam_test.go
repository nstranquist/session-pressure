package sessionpressure

import (
	"testing"
	"time"
)

// TestSelectWorkWaiterReplaySeam freezes the selector arity used by workreplay
// and evaluation. Changing selectWorkWaiter without updating ReplayWorkEvents
// must fail to compile or fail this call-site contract.
func TestSelectWorkWaiterReplaySeam(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	waiters := []WorkWaiterRecord{
		{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Weight: 1, Class: WorkClassExpressTest, QueuedAt: now},
		{OperationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Weight: 5, Class: WorkClassBuild, QueuedAt: now.Add(time.Second)},
	}
	// Same call shape as workreplay.go (candidate mode) and workeval.
	decision := selectWorkWaiter(waiters, 0, 8, now, workGreenExpressWindow{}, 0)
	if decision.SelectedOperationID == "" && decision.DecisionReason == "" {
		t.Fatal("selector must return a decision reason even when empty")
	}
	// Green overcommit path also uses expressLeased.
	green := workGreenExpressWindow{Active: true, Overcommit: workGreenExpressOvercommit}
	_ = selectWorkWaiter(waiters, 8, 8, now, green, 0)
	_ = selectWorkWaiter(waiters, 7, 8, now, green, 1)
	// Replay harness must remain linked.
	_ = ReplayWorkEvents(nil, defaultWorkLimits(8))
}
