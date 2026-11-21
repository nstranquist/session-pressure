package sessionpressure

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func emptyReportFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "exit-report")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkExitReportRoundTrip(t *testing.T) {
	path := emptyReportFile(t)
	if _, ok, err := ReadWorkExitReport(path); ok || err != nil {
		t.Fatalf("empty report: ok=%v err=%v", ok, err)
	}
	code := 2
	if err := WriteWorkExitReport(path, WorkExitReport{Source: WorkExitSourceChild, ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	report, ok, err := ReadWorkExitReport(path)
	if err != nil || !ok || report.Source != WorkExitSourceChild || report.SchemaVersion != WorkExitReportSchemaVersion || report.ExitCode == nil || *report.ExitCode != 2 {
		t.Fatalf("report=%+v ok=%v err=%v", report, ok, err)
	}
	if !report.AppliesTo(2) || report.AppliesTo(1) {
		t.Fatal("a report with an exit code applies only to that code")
	}
	// A second write replaces the first (a failed nested exec rewrites it).
	if err := WriteWorkExitReport(path, WorkExitReport{Source: WorkExitSourceSessionPressure}); err != nil {
		t.Fatal(err)
	}
	report, ok, err = ReadWorkExitReport(path)
	if err != nil || !ok || report.Source != WorkExitSourceSessionPressure || report.ExitCode != nil || !report.AppliesTo(7) {
		t.Fatalf("rewritten report=%+v ok=%v err=%v", report, ok, err)
	}
}

func TestWriteWorkExitReportNeverCreatesOrFollows(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	if err := WriteWorkExitReport(missing, WorkExitReport{Source: WorkExitSourceChild}); err == nil {
		t.Fatal("report created a missing file")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing report path exists: %v", err)
	}
	target := emptyReportFile(t)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkExitReport(link, WorkExitReport{Source: WorkExitSourceChild}); err == nil {
		t.Fatal("report followed a symbolic link")
	}
	if err := WriteWorkExitReport(dir, WorkExitReport{Source: WorkExitSourceChild}); err == nil {
		t.Fatal("report wrote to a directory")
	}
	if err := WriteWorkExitReport(target, WorkExitReport{Source: "parent"}); err == nil {
		t.Fatal("unknown source accepted")
	}
	if err := WriteWorkExitReport("", WorkExitReport{Source: WorkExitSourceChild}); err != nil {
		t.Fatalf("empty path must be a no-op: %v", err)
	}
}

func TestReadWorkExitReportRejectsUnknownContent(t *testing.T) {
	for name, content := range map[string]string{
		"future schema":  `{"schema_version":2,"source":"child"}`,
		"unknown source": `{"schema_version":1,"source":"parent"}`,
		"unknown field":  `{"schema_version":1,"source":"child","extra":1}`,
		"not json":       `child`,
		"too large":      `{"schema_version":1,"source":"child"}` + string(bytes.Repeat([]byte(" "), workExitReportMaxBytes)),
	} {
		t.Run(name, func(t *testing.T) {
			path := emptyReportFile(t)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := ReadWorkExitReport(path); ok || err == nil {
				t.Fatalf("accepted %q: ok=%v err=%v", name, ok, err)
			}
		})
	}
}

func TestTakeWorkExitReportPathRemovesTheVariable(t *testing.T) {
	t.Setenv(WorkExitReportEnv, "/tmp/report")
	if got := TakeWorkExitReportPath(); got != "/tmp/report" {
		t.Fatalf("path=%q", got)
	}
	if _, set := os.LookupEnv(WorkExitReportEnv); set {
		t.Fatal("wrapped commands would inherit the report path")
	}
	t.Setenv(WorkExitReportEnv, "relative/report")
	if got := TakeWorkExitReportPath(); got != "" {
		t.Fatalf("relative path accepted: %q", got)
	}
}

// The reason for the report: a wrapped command that exits 2 and a work run
// that refuses its own options both return 2. Only the error tells them apart.
func TestWorkExitSourceSeparatesChildFromOwnStatus(t *testing.T) {
	coordinator := NewWorkCoordinator(t.TempDir(), testWorkLimits())
	run := func(options WorkRunOptions) (int, error) {
		var stderr bytes.Buffer
		return RunWorkCommand(coordinator, options,
			func() Admission { return Admission{Allowed: true, Level: LevelNormal} },
			time.Millisecond,
			WorkRunStreams{Stdout: io.Discard, Stderr: &stderr, CommandFactory: func(target string, args []string) (*exec.Cmd, io.WriteCloser, error) {
				return exec.Command(target, args...), nil, nil
			}})
	}
	code, err := run(WorkRunOptions{Class: WorkClassExpressTest, Wait: testWaitNotUnderTest, Command: []string{"/bin/sh", "-c", "exit 2"}})
	if code != 2 || WorkExitSourceFor(err) != WorkExitSourceChild {
		t.Fatalf("child exit 2: code=%d err=%v", code, err)
	}
	code, err = run(WorkRunOptions{Class: WorkClassExpressTest, Wait: testWaitNotUnderTest, Workload: "no-such-workload", Command: []string{"/bin/sh", "-c", "exit 0"}})
	if code != 2 || WorkExitSourceFor(err) != WorkExitSourceSessionPressure {
		t.Fatalf("own usage error: code=%d err=%v", code, err)
	}
}

// A nested run replaces work run with the wrapped command, so it records the
// child as the source before the exec; a failed exec rewrites the report.
func TestNestedWorkRunReportsChildBeforeExec(t *testing.T) {
	coordinator := NewWorkCoordinator(t.TempDir(), testWorkLimits())
	ctx := context.Background()
	lease, status, err := coordinator.Acquire(ctx, WorkClassBuild)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release(ctx) }()
	parent := status.Leases[0]
	stubNestedProcess(t, parent.PID, true)
	t.Setenv(WorkOperationEnv, parent.OperationID)
	path := emptyReportFile(t)
	var seen WorkExitReport
	previousExec := workNestedExec
	workNestedExec = func(string, []string, []string) error {
		report, ok, readErr := ReadWorkExitReport(path)
		if !ok || readErr != nil {
			t.Errorf("no report before exec: ok=%v err=%v", ok, readErr)
		}
		seen = report
		return nil
	}
	t.Cleanup(func() { workNestedExec = previousExec })
	code, err := RunWorkCommandWithExpressReuse(coordinator,
		WorkRunOptions{Class: WorkClassBuild, Wait: testWaitNotUnderTest, Command: []string{"true"}, ExitReportPath: path},
		func() Admission { return Admission{Allowed: true, Level: LevelNormal} },
		time.Millisecond, WorkRunStreams{Stdout: io.Discard, Stderr: io.Discard})
	if code != 0 || err != nil {
		t.Fatalf("nested run code=%d err=%v", code, err)
	}
	if seen.Source != WorkExitSourceChild || seen.ExitCode != nil {
		t.Fatalf("report before exec = %+v", seen)
	}
}
