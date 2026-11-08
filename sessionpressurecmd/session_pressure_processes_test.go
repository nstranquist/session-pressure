package sessionpressurecmd

import (
	"encoding/json"
	"github.com/nstranquist/session-pressure/sessionpressure"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessIncidentsCLIListAndHelp(t *testing.T) {
	t.Setenv(sessionpressure.DataDirEnv, t.TempDir())
	code, out, stderr := captureMainOutput(t, func() int { return cmdSessionPressure(&Flags{JSON: true}, []string{"processes", "list"}) })
	if code != 0 || stderr != "" {
		t.Fatal(code, out, stderr)
	}
	var result struct {
		OK     bool                                  `json:"ok"`
		Report sessionpressure.ProcessIncidentReport `json:"report"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || !result.OK || result.Report.SchemaVersion != 1 || result.Report.Incidents == nil {
		t.Fatal(out, err)
	}
	code, out, _ = captureMainOutput(t, func() int { return cmdSessionPressure(&Flags{}, []string{"processes", "--help"}) })
	if code != 0 || !strings.Contains(out, "--confirm") || !strings.Contains(out, "--timeout") || !strings.Contains(out, "60%") {
		t.Fatal(code, out)
	}
	if !strings.Contains(sessionPressureHelp, "processes list|stop|run|record") {
		t.Fatal("root help omits processes")
	}
	for _, leaf := range []string{"list", "stop", "run", "record"} {
		if got := sessionpressure.CommandLeaf([]string{"processes", leaf}); got != "processes."+leaf {
			t.Fatal(got)
		}
	}
}

func TestProcessIncidentsCLIRejectsUnsafeInput(t *testing.T) {
	t.Setenv(sessionpressure.DataDirEnv, t.TempDir())
	for _, args := range [][]string{
		{"processes", "stop", "--id", "fixture"},
		{"processes", "stop", "--confirm"},
		{"processes", "stop", "--id"},
		{"processes", "stop", "--id", "one", "--id", "two", "--confirm"},
		{"processes", "stop", "--all", "--confirm"},
		{"processes", "run", "--timeout", "0", "--", "sleep", "1"},
		{"processes", "run", "--timeout", "2h", "--", "sleep", "1"},
		{"processes", "run", "--timeout", "1m", "sleep", "1"},
		{"processes", "list", "--all"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			code, _, _ := captureMainOutput(t, func() int { return cmdSessionPressure(&Flags{JSON: true}, args) })
			if code != 2 {
				t.Fatal(code)
			}
		})
	}
}

func TestProcessIncidentsCLIHistoricalReceipt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sessionpressure.DataDirEnv, dir)
	path := filepath.Join(t.TempDir(), "receipt.json")
	for _, body := range []string{`{"pid":8523,"ppid":8475,"executable":"python3","at":"2026-11-08T16:17:23.646219Z","signal":"TERM","argv":"private"}`, `{} {}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		code, _, _ := captureMainOutput(t, func() int {
			return cmdSessionPressure(&Flags{JSON: true}, []string{"processes", "record", "--receipt", path})
		})
		if code != 2 {
			t.Fatal(code)
		}
	}
	body := `{"pid":8523,"ppid":8475,"executable":"python3","at":"2026-11-08T16:17:23.646219Z","signal":"TERM"}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := captureMainOutput(t, func() int {
		return cmdSessionPressure(&Flags{JSON: true}, []string{"processes", "record", "--receipt", path})
	})
	if code != 0 || !strings.Contains(out, "operator_reported_exit") || !strings.Contains(out, "recorded_closed") {
		t.Fatal(code, out)
	}
}
