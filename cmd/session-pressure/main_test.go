package main

import (
	"reflect"
	"testing"

	"github.com/nstranquist/session-pressure/sessionpressurecmd"
)

func TestParseArgsStripsJSONFlag(t *testing.T) {
	jsonOutput, rest := sessionpressurecmd.ParseArgs([]string{"--json", "doctor"})
	if !jsonOutput {
		t.Fatal("expected --json")
	}
	if !reflect.DeepEqual(rest, []string{"doctor"}) {
		t.Fatalf("rest = %#v", rest)
	}
}

func TestParseArgsWithoutJSON(t *testing.T) {
	jsonOutput, rest := sessionpressurecmd.ParseArgs([]string{"status", "--live"})
	if jsonOutput {
		t.Fatal("did not expect --json")
	}
	if !reflect.DeepEqual(rest, []string{"status", "--live"}) {
		t.Fatalf("rest = %#v", rest)
	}
}

func TestParseArgsAcceptsDesktopNdevPressurePairing(t *testing.T) {
	jsonOutput, rest := sessionpressurecmd.ParseArgs([]string{"--json", "session", "pressure", "doctor"})
	if !jsonOutput || !reflect.DeepEqual(rest, []string{"doctor"}) {
		t.Fatalf("desktop pairing = json=%v rest=%#v", jsonOutput, rest)
	}
}

func TestParseArgsDoesNotStealChildJSONAfterDashDash(t *testing.T) {
	jsonOutput, rest := sessionpressurecmd.ParseArgs([]string{
		"work", "run", "--class", "heavy", "--", "ndev", "--json", "telemetry", "self-improve", "run",
	})
	if jsonOutput {
		t.Fatal("child --json after -- must not enable product JSON")
	}
	if !reflect.DeepEqual(rest, []string{
		"work", "run", "--class", "heavy", "--", "ndev", "--json", "telemetry", "self-improve", "run",
	}) {
		t.Fatalf("rest = %#v", rest)
	}
}
