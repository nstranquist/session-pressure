package sessionpressure

import (
	"errors"
	"strings"
	"testing"
)

func TestWorkRefusalContract(t *testing.T) {
	cause := errors.New("heavy work blocked at red/cpu: host CPU test surge")
	for name, tc := range map[string]struct {
		err  error
		kind string // "" means no refusal
	}{
		"admission denied":  {admissionRefusal(workExitPolicyDenied, cause), WorkRefusalAdmission},
		"wait timeout":      {admissionRefusal(workExitWaitTimeout, cause), WorkRefusalWaitTimeout},
		"caller interrupt":  {admissionRefusal(workExitInterrupted, cause), ""},
		"browser":           {workRefusal(WorkRefusalBrowser, cause), WorkRefusalBrowser},
		"joined with other": {errors.Join(admissionRefusal(workExitPolicyDenied, cause), errors.New("record failed")), WorkRefusalAdmission},
		"plain failure":     {cause, ""},
		"nil":               {workRefusal(WorkRefusalBrowser, nil), ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := IsWorkRefusal(tc.err); got != (tc.kind != "") {
				t.Fatalf("IsWorkRefusal = %v for %v", got, tc.err)
			}
			if tc.kind != "" && !strings.Contains(tc.err.Error(), WorkRefusalMarker+" kind="+tc.kind+": ") {
				t.Fatalf("missing %s marker: %v", tc.kind, tc.err)
			}
		})
	}
	// Callers match the literal; keep it stable.
	if WorkRefusalMarker != "ndev-work-refused/v1" {
		t.Fatal("refusal marker is a cross-repo contract; version it instead of editing it")
	}
}
