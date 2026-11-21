package sessionpressure

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// WorkExitReportEnv names a file in which work run records which side chose
// its exit status. `work run` exits with the wrapped command's status, so a
// wrapper that sees only the number cannot tell a wrapped command's exit 2
// from a session-pressure usage error. The wrapper creates the file (mode
// 0600, empty) before it starts work run and reads it after work run exits.
// Work run never creates the file, and it removes the variable from its own
// environment so no wrapped command inherits it.
const WorkExitReportEnv = "SESSION_PRESSURE_WORK_EXIT_REPORT"

// WorkExitReportSchemaVersion is the only report version a reader accepts.
const WorkExitReportSchemaVersion = 1

// workExitReportMaxBytes bounds a read; a valid report is under 100 bytes.
const workExitReportMaxBytes = 4096

// WorkExitSource says which side set a work run exit status.
type WorkExitSource string

const (
	// WorkExitSourceChild: the wrapped command set the status. This covers a
	// local child, a remote runner job, a replayed reuse receipt of a child,
	// a nested run that replaced work run, and a child that died from a
	// signal work run forwarded to it.
	WorkExitSourceChild WorkExitSource = "child"
	// WorkExitSourceSessionPressure: session-pressure set the status itself
	// (arguments, policy, admission, transport or another internal failure).
	WorkExitSourceSessionPressure WorkExitSource = "session_pressure"
)

func (s WorkExitSource) valid() bool {
	return s == WorkExitSourceChild || s == WorkExitSourceSessionPressure
}

// WorkExitReport is the file content behind WorkExitReportEnv.
type WorkExitReport struct {
	SchemaVersion int            `json:"schema_version"`
	Source        WorkExitSource `json:"source"`
	// ExitCode is the status work run returned. It is nil when the wrapped
	// command replaced work run (a nested run), so only the wrapper sees it.
	ExitCode *int `json:"exit_code,omitempty"`
}

// AppliesTo reports whether the report describes an observed exit status.
// A report whose code differs (for example, work run was killed after it
// wrote the report) does not apply.
func (r WorkExitReport) AppliesTo(observed int) bool {
	return r.ExitCode == nil || *r.ExitCode == observed
}

// TakeWorkExitReportPath returns the report path from this process
// environment and removes the variable, so wrapped commands never inherit
// it. A relative path is ignored.
func TakeWorkExitReportPath() string {
	path := workExitReportPath()
	_ = os.Unsetenv(WorkExitReportEnv)
	return path
}

func workExitReportPath() string {
	if path := os.Getenv(WorkExitReportEnv); filepath.IsAbs(path) {
		return path
	}
	return ""
}

// ReportOwnWorkExit records a status that the CLI chose before it started the
// work run helper (an argument it refused, or a helper it could not start).
// The variable stays set, so a helper that does start writes its own report.
// It returns code, and prints a write failure to stderr.
func ReportOwnWorkExit(code int) int {
	if err := WriteWorkExitReport(workExitReportPath(), WorkExitReport{Source: WorkExitSourceSessionPressure, ExitCode: &code}); err != nil {
		fmt.Fprintln(os.Stderr, "session-pressure:", err)
	}
	return code
}

// WriteWorkExitReport replaces the content of the wrapper's report file. An
// empty path is a no-op. It opens an existing regular file only: it never
// creates a file and never follows a symbolic link.
func WriteWorkExitReport(path string, report WorkExitReport) error {
	if path == "" {
		return nil
	}
	if !report.Source.valid() {
		return fmt.Errorf("work exit report: unknown source %q", report.Source)
	}
	report.SchemaVersion = WorkExitReportSchemaVersion
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("work exit report: %w", err)
	}
	if info, statErr := f.Stat(); statErr != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return errors.Join(errors.New("work exit report: not a regular file"), statErr)
	}
	_, err = f.Write(append(data, '\n'))
	return errors.Join(err, f.Close())
}

// ReadWorkExitReport reads the report a work run wrote. ok is false when the
// file is empty: an older work run, or a run that ended before it wrote one.
func ReadWorkExitReport(path string) (report WorkExitReport, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return WorkExitReport{}, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, workExitReportMaxBytes+1))
	if err != nil {
		return WorkExitReport{}, false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return WorkExitReport{}, false, nil
	}
	if len(data) > workExitReportMaxBytes {
		return WorkExitReport{}, false, errors.New("work exit report: too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return WorkExitReport{}, false, fmt.Errorf("work exit report: %w", err)
	}
	if report.SchemaVersion != WorkExitReportSchemaVersion || !report.Source.valid() {
		return WorkExitReport{}, false, fmt.Errorf("work exit report: unsupported schema %d or source %q", report.SchemaVersion, report.Source)
	}
	return report, true, nil
}

// WorkExitSourceFor classifies a work run result: session-pressure returns an
// error with every status it chooses itself, and a nil error with a status
// that the wrapped command produced.
func WorkExitSourceFor(err error) WorkExitSource {
	if err != nil {
		return WorkExitSourceSessionPressure
	}
	return WorkExitSourceChild
}
