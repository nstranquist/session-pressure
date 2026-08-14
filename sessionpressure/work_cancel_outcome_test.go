package sessionpressure

import (
	"context"
	"errors"
	"syscall"
	"testing"
)

func TestWorkCancelOutcomeClosedEnums(t *testing.T) {
	if got := workCancelOutcome(nil, syscall.SIGTERM); got != "wrapper_interrupt" {
		t.Fatalf("SIGTERM=%q", got)
	}
	if got := workCancelOutcome(nil, syscall.SIGINT); got != "wrapper_interrupt" {
		t.Fatalf("SIGINT=%q", got)
	}
	if got := workCancelOutcome(context.Canceled, 0); got != "wrapper_interrupt" {
		t.Fatalf("canceled=%q", got)
	}
	if got := workCancelOutcome(context.DeadlineExceeded, 0); got != "interrupted" {
		t.Fatalf("deadline=%q", got)
	}
	if got := workCancelOutcome(errors.New("other"), 0); got != "interrupted" {
		t.Fatalf("other=%q", got)
	}
	// Signal path outranks err
	if got := workCancelOutcome(context.DeadlineExceeded, syscall.SIGTERM); got != "wrapper_interrupt" {
		t.Fatalf("signal wins=%q", got)
	}
}

func TestIsWrapperInterruptAcceptsLegacySignalForwarded(t *testing.T) {
	if !IsWrapperInterruptEvent(WorkEvent{Event: WorkEventCancelled, Outcome: "signal_forwarded"}) {
		t.Fatal("legacy signal_forwarded must still count")
	}
}
