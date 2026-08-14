package sessionpressure

import (
	"strings"
	"testing"
)

func TestCanonicalMakeBuildSessionPressureUsesExtractDaemon(t *testing.T) {
	commands, err := canonicalMakeBuildGoCommands("build-ndev-session-pressure", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) == 0 {
		t.Fatal("no canonical commands")
	}
	joined := strings.Join(commands[0], " ")
	if !strings.Contains(joined, "github.com/nstranquist/session-pressure/sessionpressure/daemon") {
		t.Fatalf("resident snapshot = %q", joined)
	}
	if strings.Contains(joined, "./internal/sessionpressure/daemon") {
		t.Fatalf("still snapshots deleted factory daemon path: %q", joined)
	}
}

func TestCanonicalMakeBuildAllIncludesExtractDaemon(t *testing.T) {
	commands, err := canonicalMakeBuildGoCommands("build-ndev-all", nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, command := range commands {
		if strings.Join(command, " ") == "go build github.com/nstranquist/session-pressure/sessionpressure/daemon" {
			found = true
		}
	}
	if !found {
		t.Fatalf("build-ndev-all missing extract daemon: %#v", commands)
	}
}
