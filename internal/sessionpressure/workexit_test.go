package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// Coordinator-origin failures must exit in the classified bands (124 timeout,
// 130 interrupt, 11 policy denial) so red-pressure waits stop projecting as
// unclassifiable exit-1 failures; child exit codes are untouched by these.
// Classification keys on the error chain, not ambient context state.
func TestCoordinatorFailureExitClassification(t *testing.T) {
	wrappedDeadline := fmt.Errorf("wait for host pressure admission: %w", context.DeadlineExceeded)
	if got := coordinatorFailureExit(wrappedDeadline, workExitPolicyDenied); got != workExitWaitTimeout {
		t.Fatalf("deadline-exceeded exit = %d, want %d", got, workExitWaitTimeout)
	}

	wrappedCancel := fmt.Errorf("wait for host pressure admission: %w", context.Canceled)
	if got := coordinatorFailureExit(wrappedCancel, 1); got != workExitInterrupted {
		t.Fatalf("cancelled exit = %d, want %d", got, workExitInterrupted)
	}

	denial := errors.New("heavy work blocked at red/cpu: host CPU is red")
	if got := coordinatorFailureExit(denial, workExitPolicyDenied); got != workExitPolicyDenied {
		t.Fatalf("no-wait denial exit = %d, want %d", got, workExitPolicyDenied)
	}
	if got := coordinatorFailureExit(errors.New("coordinator broke"), 1); got != 1 {
		t.Fatalf("internal-error fallback exit = %d, want 1", got)
	}
	if got := coordinatorFailureExit(nil, 1); got != 1 {
		t.Fatalf("nil-cause fallback exit = %d, want 1", got)
	}
}
