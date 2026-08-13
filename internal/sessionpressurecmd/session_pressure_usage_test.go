package sessionpressurecmd

import (
	"strings"
	"testing"
)

func TestSessionPressureUnknownSubcommandNamesHelp(t *testing.T) {
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{}, []string{"--bogus"})
	})
	if rc != 2 {
		t.Fatalf("rc = %d want 2; stderr=%q", rc, stderr)
	}
	if !strings.Contains(stderr, `unknown subcommand "--bogus"`) {
		t.Fatalf("stderr missing unknown-subcommand line: %q", stderr)
	}
	if !strings.Contains(stderr, "session pressure --help") || !strings.Contains(stderr, "status") {
		t.Fatalf("stderr missing help hint: %q", stderr)
	}
}

func TestSessionPressureUnknownWorkSubcommandNamesHelp(t *testing.T) {
	rc, _, stderr := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{}, []string{"work", "--bogus"})
	})
	if rc != 2 {
		t.Fatalf("rc = %d want 2; stderr=%q", rc, stderr)
	}
	if !strings.Contains(stderr, `unknown work subcommand "--bogus"`) {
		t.Fatalf("stderr missing unknown work subcommand: %q", stderr)
	}
	if !strings.Contains(stderr, "work --help") || !strings.Contains(stderr, "status") {
		t.Fatalf("stderr missing work help hint: %q", stderr)
	}
}
