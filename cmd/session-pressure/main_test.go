package main

import (
	"reflect"
	"testing"

	"github.com/nstranquist/session-pressure/internal/sessionpressurecmd"
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
