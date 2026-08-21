package sessionpressure

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInspectWorkNamesLiveCommandAndDoesNotWriteEvents(t *testing.T) {
	dir := t.TempDir()
	coordinator := NewWorkCoordinator(dir, DefaultPolicy(16<<10).WorkLimits)
	child := exec.Command("sleep", "30")
	child.Env = append(os.Environ(), "NDEV_SCHEDULE_ID=docs-corpus-local-refresh", "NDEV_SCHEDULE_RUN_ID=42")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()

	lease, _, err := coordinator.Acquire(context.Background(), WorkClassExpressTest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release(context.Background()) }()
	if err := lease.BindPID(context.Background(), child.Process.Pid); err != nil {
		t.Fatal(err)
	}

	report, err := InspectWork(context.Background(), coordinator, lease.Record().OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Process == nil || !report.Process.Alive || report.Process.PID != child.Process.Pid {
		t.Fatalf("process=%#v", report.Process)
	}
	if !strings.Contains(report.Process.Command, "sleep") {
		t.Fatalf("command=%q", report.Process.Command)
	}
	if report.Origin.Kind == "schedule" && report.Origin.ScheduleID != "docs-corpus-local-refresh" {
		t.Fatalf("origin=%#v", report.Origin)
	}
	if report.Lease == nil || report.Summary == "" || report.StallClass == "" {
		t.Fatalf("report=%#v", report)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "work-events-*.jsonl"))
	for _, path := range matches {
		raw, _ := os.ReadFile(path)
		if strings.Contains(string(raw), "sleep") || strings.Contains(string(raw), "docs-corpus-local-refresh") {
			t.Fatalf("inspect leaked command/schedule into work events %s:\n%s", path, raw)
		}
	}
}

func TestCancelWorkStopsSetpgidChildAndPrunesLease(t *testing.T) {
	dir := t.TempDir()
	coordinator := NewWorkCoordinator(dir, DefaultPolicy(16<<10).WorkLimits)
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	lease, _, err := coordinator.Acquire(context.Background(), WorkClassExpressTest)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.BindPID(context.Background(), pid); err != nil {
		t.Fatal(err)
	}

	result, err := CancelWork(context.Background(), coordinator, lease.Record().OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Work.Used != 0 {
		t.Fatalf("used=%d after cancel; expected prune", result.Work.Used)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = child.Process.Kill()
		t.Fatalf("pid %d still running after cancel signaled=%v", pid, result.SignaledPIDs)
	}
}

func TestHandoffWorkWritesOperatorBrief(t *testing.T) {
	origCapture := captureScratchpadAgents
	t.Cleanup(func() { captureScratchpadAgents = origCapture })
	captureScratchpadAgents = func(header, body string) (string, error) {
		if !strings.Contains(body, "sleep") {
			t.Fatalf("unexpected brief: %s", body)
		}
		return "/tmp/agents-work-inspect.md", nil
	}
	dir := t.TempDir()
	t.Setenv(DataDirEnv, dir)
	coordinator := NewWorkCoordinator(dir, DefaultPolicy(16<<10).WorkLimits)
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	lease, _, err := coordinator.Acquire(context.Background(), WorkClassTest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release(context.Background()) }()
	if err := lease.BindPID(context.Background(), child.Process.Pid); err != nil {
		t.Fatal(err)
	}
	handoff, err := HandoffWork(context.Background(), coordinator, lease.Record().OperationID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(handoff.Path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "sleep") || !strings.Contains(body, lease.Record().OperationID) {
		t.Fatalf("brief=%s", body)
	}
	if handoff.ScratchpadPath != "/tmp/agents-work-inspect.md" || handoff.ScratchpadTarget != "agents" {
		t.Fatalf("scratchpad=%#v", handoff)
	}
}

func TestInspectWorkJoinsScheduleOriginFromLeaseEnviron(t *testing.T) {
	t.Setenv("NDEV_SCHEDULE_ID", "docs-corpus-local-refresh")
	t.Setenv("NDEV_SCHEDULE_RUN_ID", "9")
	origLookup := lookupScheduleName
	t.Cleanup(func() { lookupScheduleName = origLookup })
	lookupScheduleName = func(id, runID string) (string, string) {
		return "Local Documentation Corpus Refresh", "building standalone docs-puller"
	}
	dir := t.TempDir()
	coordinator := NewWorkCoordinator(dir, DefaultPolicy(16<<10).WorkLimits)
	lease, _, err := coordinator.Acquire(context.Background(), WorkClassExpressTest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release(context.Background()) }()
	report, err := InspectWork(context.Background(), coordinator, lease.Record().OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Origin.Kind != "schedule" || report.Origin.ScheduleID != "docs-corpus-local-refresh" {
		t.Fatalf("origin=%#v", report.Origin)
	}
	if report.Origin.ScheduleName != "Local Documentation Corpus Refresh" {
		t.Fatalf("name=%q", report.Origin.ScheduleName)
	}
}

func TestInspectWorkJoinsScheduleOriginFromEnv(t *testing.T) {
	orig := readProcessEnv
	t.Cleanup(func() { readProcessEnv = orig })
	readProcessEnv = func(int, []string) map[string]string {
		return map[string]string{"NDEV_SCHEDULE_ID": "docs-corpus-local-refresh", "NDEV_SCHEDULE_RUN_ID": "9"}
	}
	origLookup := lookupScheduleName
	t.Cleanup(func() { lookupScheduleName = origLookup })
	lookupScheduleName = func(id, runID string) (string, string) {
		return "Local Documentation Corpus Refresh", "building standalone docs-puller"
	}

	dir := t.TempDir()
	coordinator := NewWorkCoordinator(dir, DefaultPolicy(16<<10).WorkLimits)
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	lease, _, err := coordinator.Acquire(context.Background(), WorkClassExpressTest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release(context.Background()) }()
	if err := lease.BindPID(context.Background(), child.Process.Pid); err != nil {
		t.Fatal(err)
	}
	report, err := InspectWork(context.Background(), coordinator, lease.Record().OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Origin.Kind != "schedule" || report.Origin.ScheduleID != "docs-corpus-local-refresh" {
		t.Fatalf("origin=%#v", report.Origin)
	}
	if report.Origin.ScheduleName != "Local Documentation Corpus Refresh" {
		t.Fatalf("name=%q", report.Origin.ScheduleName)
	}
	if !strings.Contains(report.Summary, "Local Documentation Corpus Refresh") && !strings.Contains(report.Summary, "docs-corpus") {
		t.Fatalf("summary=%q", report.Summary)
	}
	cancel := false
	for _, action := range report.SuggestedActions {
		if action.ID == "reschedule" && action.Available {
			cancel = true
		}
	}
	if !cancel {
		t.Fatalf("actions=%#v", report.SuggestedActions)
	}
}

func TestInspectWorkUnknownOperation(t *testing.T) {
	coordinator := NewWorkCoordinator(t.TempDir(), DefaultPolicy(16<<10).WorkLimits)
	_, err := InspectWork(context.Background(), coordinator, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil || !strings.Contains(err.Error(), "not a live lease") {
		t.Fatalf("err=%v", err)
	}
}
