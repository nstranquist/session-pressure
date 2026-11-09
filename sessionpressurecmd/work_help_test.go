package sessionpressurecmd

import (
	"strings"
	"testing"
)

func TestWorkRunHelpDoesNotStartHelper(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		rc, out, errOut := captureMainOutput(t, func() int {
			return cmdSessionPressureWork(&Flags{}, []string{"run", flag})
		})
		if rc != 0 || !strings.Contains(out, "run [--class") || errOut != "" {
			t.Fatalf("help: rc=%d stdout=%q stderr=%q", rc, out, errOut)
		}
	}
	// Help in a child argv must reach normal child validation, not print help.
	rc, out, _ := captureMainOutput(t, func() int {
		return cmdSessionPressureWork(&Flags{JSON: true}, []string{"run", "--", "echo", "--help"})
	})
	if rc != 2 || strings.Contains(out, "run [--class") {
		t.Fatalf("child help intercepted: rc=%d stdout=%q", rc, out)
	}
}
