package sessionpressurecmd

import (
	"strings"
	"testing"

	"github.com/nstranquist/session-pressure/internal/sessionpressure"
)

// A pressure-check denial is a policy decision: it must exit in the reserved
// policy band (11) so invocation telemetry classifies it policy_block instead
// of an unclassifiable exit-1 failure.
func TestSessionPressureCheckDenialExitsPolicyBand(t *testing.T) {
	previous := AgentLaunchAdmissionCheck
	AgentLaunchAdmissionCheck = func(sessionpressure.AgentLaunchKind) sessionpressure.Admission {
		return sessionpressure.Admission{
			Allowed: false, Level: sessionpressure.LevelRed, Source: "fixture",
			Reasons: []string{"host CPU fixture is red"},
		}
	}
	t.Cleanup(func() { AgentLaunchAdmissionCheck = previous })
	rc, stdout, _ := captureMainOutput(t, func() int {
		return cmdSessionPressureCheck(&Flags{}, nil)
	})
	if rc != 11 {
		t.Fatalf("denied check rc = %d, want 11 (policy band)", rc)
	}
	if !strings.Contains(stdout, "allowed=false") {
		t.Fatalf("denied check stdout = %q, want allowed=false", stdout)
	}

	AgentLaunchAdmissionCheck = func(sessionpressure.AgentLaunchKind) sessionpressure.Admission {
		return sessionpressure.Admission{Allowed: true, Level: sessionpressure.LevelNormal, Source: "fixture"}
	}
	rc, _, _ = captureMainOutput(t, func() int {
		return cmdSessionPressureCheck(&Flags{}, nil)
	})
	if rc != 0 {
		t.Fatalf("allowed check rc = %d, want 0", rc)
	}
}
