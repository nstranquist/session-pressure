package sessionpressurecmd

import (
	"github.com/nstranquist/session-pressure/sessionpressure"
	"testing"
)

func TestAuditAcceptsAdmissionOnlyPolicy(t *testing.T) {
	p := sessionpressure.DefaultPolicy(24)
	p.Enabled, p.EnforceAdmission, p.AutoShedCritical = true, true, false
	report := auditPressurePolicy(pressureRuntime{policy: p, persisted: true})
	if report.Status != pressureAuditPass {
		t.Fatalf("admission-only policy: %+v", report)
	}
	p.EnforceAdmission = false
	report = auditPressurePolicy(pressureRuntime{policy: p, persisted: true})
	if report.Status != pressureAuditWarn {
		t.Fatalf("observation mode: %+v", report)
	}
}
