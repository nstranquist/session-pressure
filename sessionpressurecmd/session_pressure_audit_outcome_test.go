package sessionpressurecmd

import (
	"os"
	"testing"
)

func TestAuditFindingRequiresSuccessfullyEmittedReport(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		for _, healthy := range []bool{false, true} {
			g := &Flags{JSON: jsonOutput}
			code, _, _ := captureMainOutput(t, func() int {
				return emitPressureAuditReport(g, map[string]any{"ok": healthy}, "audit report\n", pressureAuditReport{OK: healthy})
			})
			if (code == 0) != healthy || g.gateNotSatisfied == healthy {
				t.Fatalf("json=%t healthy=%t exit=%d gate=%t", jsonOutput, healthy, code, g.gateNotSatisfied)
			}
		}
	}
	g := &Flags{JSON: true}
	code, _, _ := captureMainOutput(t, func() int {
		return emitPressureAuditReport(g, make(chan int), "", pressureAuditReport{OK: false})
	})
	if code != 1 || g.gateNotSatisfied {
		t.Fatalf("encoding error became gate finding: code=%d flags=%+v", code, g)
	}
}

func TestAuditOutputFailureRemainsCommandFailure(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		func() {
			closed, err := os.CreateTemp(t.TempDir(), "closed-output")
			if err != nil {
				t.Fatal(err)
			}
			if err := closed.Close(); err != nil {
				t.Fatal(err)
			}
			previous := os.Stdout
			os.Stdout = closed
			defer func() { os.Stdout = previous }()
			g := &Flags{JSON: jsonOutput}
			code := emitPressureAuditReport(g, map[string]any{"ok": false}, "audit report\n", pressureAuditReport{OK: false})
			if code != 1 || g.gateNotSatisfied {
				t.Fatalf("output error became gate finding: code=%d flags=%+v", code, g)
			}
		}()
	}
}
