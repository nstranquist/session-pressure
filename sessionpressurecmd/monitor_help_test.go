package sessionpressurecmd

import (
	"context"
	"strings"
	"testing"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestMonitorHelpDoesNotLoadOrChangeResident(t *testing.T) {
	prior := NewLaunchdController
	NewLaunchdController = func(string) (LaunchdController, error) {
		t.Fatal("help must not construct a resident controller")
		return nil, context.Canceled
	}
	t.Cleanup(func() { NewLaunchdController = prior })
	t.Setenv("NDEV_SESSION_PRESSURE_HOME", t.TempDir())
	for _, path := range [][]string{{"monitor", "help"}, {"monitor", "--help"}, {"monitor", "-h"}, {"monitor", "install", "--help"}, {"monitor", "uninstall", "-h"}} {
		rc, out, errOut := captureMainOutput(t, func() int { return cmdSessionPressure(&Flags{}, path) })
		if rc != 0 || !strings.Contains(out, "Usage: session-pressure monitor") || errOut != "" {
			t.Fatalf("%v: rc=%d stdout=%q stderr=%q", path, rc, out, errOut)
		}
		if got := sessionpressure.CommandLeaf(path); got != "monitor.help" {
			t.Fatalf("%v: direct leaf=%q", path, got)
		}
		if got := sessionpressure.CommandLeafFromCLIArgs(append([]string{"--json", "session", "pressure"}, path...)); got != "monitor.help" {
			t.Fatalf("%v: frontdoor leaf=%q", path, got)
		}
	}
}
