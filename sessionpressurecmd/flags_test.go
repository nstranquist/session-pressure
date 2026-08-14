package sessionpressurecmd

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseArgsAcceptsDesktopProductCLIPairing(t *testing.T) {
	// Desktop resolveBinary prefers ndev-pressure but still sends the ndev path:
	// `--json session pressure <leaf>`. The product CLI must not treat
	// "session" as a subcommand.
	jsonOut, rest := ParseArgs([]string{"--json", "session", "pressure", "doctor"})
	if !jsonOut {
		t.Fatal("expected --json")
	}
	if !reflect.DeepEqual(rest, []string{"doctor"}) {
		t.Fatalf("paired argv rest = %#v, want [doctor]", rest)
	}

	jsonOut, rest = ParseArgs([]string{"--json", "session", "pressure", "status", "--live"})
	if !jsonOut || !reflect.DeepEqual(rest, []string{"status", "--live"}) {
		t.Fatalf("status pairing = %v %#v", jsonOut, rest)
	}

	jsonOut, rest = ParseArgs([]string{"--json", "doctor"})
	if !jsonOut || !reflect.DeepEqual(rest, []string{"doctor"}) {
		t.Fatalf("product-local argv = %v %#v", jsonOut, rest)
	}
}

func TestMainAcceptsDesktopSessionPressurePrefix(t *testing.T) {
	jsonOut, rest := ParseArgs([]string{"--json", "session", "pressure", "help"})
	rc, stdout, stderr := captureMainOutput(t, func() int {
		return Main(jsonOut, rest)
	})
	if rc != 0 {
		t.Fatalf("Main desktop pairing rc=%d stderr=%s", rc, stderr)
	}
	if strings.Contains(stderr, `unknown subcommand "session"`) {
		t.Fatalf("product CLI rejected desktop argv: %s", stderr)
	}
	if !strings.Contains(stdout, "status") || !strings.Contains(stdout, "doctor") {
		t.Fatalf("help via desktop pairing missing leaves: %s", stdout)
	}

	rc, _, stderr = captureMainOutput(t, func() int {
		return Main(true, []string{"session", "pressure", "help"})
	})
	if rc != 0 || strings.Contains(stderr, `unknown subcommand "session"`) {
		t.Fatalf("Main(session pressure help) rc=%d stderr=%s", rc, stderr)
	}
}
