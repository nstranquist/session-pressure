package sessionpressurecmd

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

// Read the dispatcher rather than duplicate its command list in a test.
func TestCapabilitiesCoverDispatcher(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "session_pressure.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]bool{}
	for _, op := range sessionpressure.CurrentCapabilities().Operations {
		operations[op] = true
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "cmdSessionPressure" {
			continue
		}
		for _, statement := range fn.Body.List {
			sw, ok := statement.(*ast.SwitchStmt)
			if !ok {
				continue
			}
			for _, clause := range sw.Body.List {
				for _, expr := range clause.(*ast.CaseClause).List {
					literal, ok := expr.(*ast.BasicLit)
					if !ok {
						continue
					}
					command, e := strconv.Unquote(literal.Value)
					if e != nil {
						t.Fatal(e)
					}
					if command == "help" || strings.HasPrefix(command, "-") {
						continue
					}
					if !operations[command] {
						t.Errorf("dispatcher command %q missing from capabilities", command)
					}
				}
			}
		}
	}
}

func TestRemoteInspectionCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rc, stdout, stderr := captureMainOutput(t, func() int { return Main(true, []string{"remote", "inspect", "--no-box"}) })
	var result struct {
		OK         bool `json:"ok"`
		Inspection struct {
			Schema       int   `json:"schema_version"`
			BoxRequested bool  `json:"box_requested"`
			Runs         []any `json:"runs"`
		} `json:"inspection"`
	}
	if e := json.Unmarshal([]byte(stdout), &result); e != nil || rc != 0 || !result.OK || result.Inspection.Schema != 1 || result.Inspection.BoxRequested || result.Inspection.Runs == nil {
		t.Fatalf("rc=%d decode=%v stdout=%s stderr=%s", rc, e, stdout, stderr)
	}
	for _, args := range [][]string{{"remote", "inspect", "--unexpected"}, {"remote", "job-log"}, {"remote", "job-log", "--id", "../bad"}} {
		rc, _, _ = captureMainOutput(t, func() int { return Main(true, args) })
		if rc == 0 {
			t.Errorf("invalid arguments accepted: %q", args)
		}
	}
	for _, leaf := range []string{"inspect", "job-log"} {
		if got := sessionpressure.CommandLeaf([]string{"remote", leaf, "--id", "private-value"}); got != "remote."+leaf {
			t.Errorf("telemetry leaf=%q", got)
		}
	}
}

func TestReviewedReadLeavesAreCategorical(t *testing.T) {
	for _, row := range []struct {
		args []string
		want string
	}{
		{[]string{"io", "health", "--private-value"}, "io.health"},
		{[]string{"identity", "show", "--live"}, "identity.show"},
	} {
		if got := sessionpressure.CommandLeaf(row.args); got != row.want {
			t.Errorf("got %q want %q", got, row.want)
		}
	}
}
