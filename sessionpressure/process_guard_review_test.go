package sessionpressure

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessGuardInitOwnedGUIUsesNormalWindow(t *testing.T) {
	g, rows, at := guardFixture(t)
	rows[0].Executable = "iTerm2"
	for i := 0; i <= 5; i++ {
		if err := g.Observe(context.Background(), at.Add(time.Duration(i)*time.Minute), rows); err != nil {
			t.Fatal(err)
		}
		r, err := ReadProcessIncidents(g.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if i < 5 && len(r.Incidents) != 0 {
			t.Fatal("GUI app received command-only threshold", r)
		}
		if i == 5 && (len(r.Incidents) != 1 || r.Incidents[0].Reason != "sustained_cpu") {
			t.Fatal(r)
		}
	}
}

func TestProcessGuardConfirmsLateExitAfterStop(t *testing.T) {
	g, _, at := guardFixture(t)
	p := activeGuardIncident(t)
	p.State = "stop_requested"
	p.Actions = []ProcessIncidentAction{{At: at, Signal: "TERM", Result: "signal_sent_unconfirmed"}}
	seedGuardIncident(t, g.Dir, p)
	// A transient identity lookup failure cannot establish an exit.
	g.identity = func(int) (string, int, error) { return "", 0, fmt.Errorf("transient fixture error") }
	if err := g.Observe(context.Background(), at, nil); err != nil {
		t.Fatal(err)
	}
	r, _ := ReadProcessIncidents(g.Dir)
	if r.Incidents[0].State != "stop_requested" {
		t.Fatal(r)
	}
	g.identity = func(int) (string, int, error) { return "", 0, errProcessGuardExited }
	if err := g.Observe(context.Background(), at.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	r, _ = ReadProcessIncidents(g.Dir)
	if r.Incidents[0].State != "stopped" || len(r.Incidents[0].Actions) != 2 || r.Incidents[0].Actions[1].Signal != "NONE" {
		t.Fatal(r)
	}
}

func readReviewPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(path); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
			if err == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned child did not publish PID")
	return 0
}

func assertReviewProcessExited(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := processGuardIdentity(pid); processGuardGone(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	birth, uid, err := processGuardIdentity(pid)
	state, psErr := exec.Command("ps", "-o", "pid,ppid,pgid,stat,comm", "-p", strconv.Itoa(pid)).CombinedOutput()
	t.Fatalf("owned descendant %d exit not confirmed: birth=%q uid=%d identity=%v signal-zero=%v ps=%s (%v)", pid, birth, uid, err, syscall.Kill(pid, 0), state, psErr)
}

func cleanupReviewProcess(pid int) func() {
	birth, uid, err := processGuardIdentity(pid)
	return func() {
		if err != nil {
			return
		}
		current, owner, currentErr := processGuardIdentity(pid)
		if currentErr == nil && current == birth && uid == owner {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func TestBoundedProcessCleansDescendantsAfterCommandExit(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	code, err := RunBoundedProcess(context.Background(), dir, 5*time.Second, []string{"/bin/sh", "-c", "sleep 60 & echo $! > \"$1\"; exit 7", "fixture", pidPath}, nil, nil, nil)
	pid := readReviewPID(t, pidPath)
	defer cleanupReviewProcess(pid)()
	if err != nil || code != 7 {
		t.Fatal(code, err)
	}
	assertReviewProcessExited(t, pid)
}

func TestBoundedProcessCleansDescendantsAfterSupervisorDeath(t *testing.T) {
	if os.Getenv("PRESSURE_GUARD_REVIEW_CHILD") == "1" {
		dir := os.Getenv("PRESSURE_GUARD_REVIEW_DIR")
		code, err := RunBoundedProcess(context.Background(), dir, time.Minute, []string{"/bin/sh", "-c", "echo $$ > \"$1\"; exec sleep 60", "fixture", filepath.Join(dir, "child.pid")}, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(code)
	}
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestBoundedProcessCleansDescendantsAfterSupervisorDeath$")
	child.Env = append(os.Environ(), "PRESSURE_GUARD_REVIEW_CHILD=1", "PRESSURE_GUARD_REVIEW_DIR="+dir)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	pid := readReviewPID(t, filepath.Join(dir, "child.pid"))
	defer cleanupReviewProcess(pid)()
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	assertReviewProcessExited(t, pid)
}
