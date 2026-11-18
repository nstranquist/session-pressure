package jsonl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests pin the three WORK-1252 defects of the old internal/lockfile.
// Each one fails on that code and passes on internal/filelock. They plant
// sidecars in the on-disk format that both versions share.

const successorToken = "successor-live-owner"

func plantSidecar(t *testing.T, lockPath string, pid int, at time.Time, token string) {
	t.Helper()
	raw := fmt.Sprintf(`{"pid":%d,"timestamp":%d,"token":%q}`+"\n", pid, at.UnixMilli(), token)
	tmp := lockPath + ".plant"
	if err := os.WriteFile(tmp, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, lockPath); err != nil {
		t.Fatal(err)
	}
}

// deadPID returns a PID with no process behind it.
func deadPID(t *testing.T) int {
	t.Helper()
	for pid := 4_000_000; pid > 3_000_000; pid -= 7919 {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return pid
		}
	}
	t.Skip("no free PID found")
	return 0
}

// assertLockKept checks that the append did not run and the sidecar still
// carries token.
func assertLockKept(t *testing.T, path string, err error, token string) {
	t.Helper()
	if err == nil {
		t.Fatal("AppendLineWithin succeeded while another owner held the lock")
	}
	if body, readErr := os.ReadFile(path); readErr == nil && len(body) > 0 {
		t.Fatalf("append ran inside another owner's lock: %q", body)
	}
	raw, readErr := os.ReadFile(path + ".lock")
	if readErr != nil {
		t.Fatalf("live sidecar was removed: %v", readErr)
	}
	if token != "" && !strings.Contains(string(raw), token) {
		t.Fatalf("live sidecar was replaced: %s", raw)
	}
}

// Defect 1: a waiter judged the sidecar stale and then removed whatever file
// was there, with no guard and no second check. A successor that published
// its live lock in between was deleted, and two writers were inside.
//
// This test pins the jsonl side: a waiter that judges a sidecar stale does
// not remove it while another process holds the reclaim guard, and after the
// guard is released it keeps a live successor. The old code removed the
// sidecar at once and appended. The second judgement under the guard, the
// exact defect-1 window, is pinned in package filelock by
// TestReclaimJudgesAgainUnderGuard, which can use the unexported seam.
func TestAppendHonoursReclaimGuardAndKeepsSuccessor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	lockPath := path + ".lock"
	plantSidecar(t, lockPath, deadPID(t), time.Now(), "dead-owner")

	guard, err := os.OpenFile(lockPath+".reclaim", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- AppendLineWithin(path, []byte(`{"q":"waiter"}`), 0o644, 400*time.Millisecond)
	}()
	time.Sleep(100 * time.Millisecond)
	plantSidecar(t, lockPath, os.Getpid(), time.Now(), successorToken)
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	assertLockKept(t, path, <-done, successorToken)
}

// Defect 2: a live owner older than 30 s counted as stale, so a long append
// (for example a writer paged out under swap) was preempted.
//
// The owner is PID 1, which has run since boot, so it started long before
// its sidecar was written. This test's own PID with an old timestamp would be
// the reused-PID case (WORK-1373), which is stale by design.
func TestAppendDoesNotPreemptAgedLiveOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	plantSidecar(t, path+".lock", 1, time.Now().Add(-time.Minute), successorToken)
	err := AppendLineWithin(path, []byte(`{"q":"waiter"}`), 0o644, 150*time.Millisecond)
	assertLockKept(t, path, err, successorToken)
}

// Defect 3: the old writer created the sidecar empty (O_EXCL) and wrote it
// afterwards. A competitor that read the empty file counted it stale and
// removed it. A fresh empty sidecar, which an n-1 writer can still show, must
// be left alone.
func TestAppendDoesNotStealSidecarDuringInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path+".lock", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := AppendLineWithin(path, []byte(`{"q":"waiter"}`), 0o644, 150*time.Millisecond)
	assertLockKept(t, path, err, "")
}

// WORK-1373: a writer killed inside an append (or a host restart) leaves its
// sidecar, and a new process can later get the same PID. That process started
// after the sidecar was written, so it is not the owner, and the ledger must
// not stay blocked until it exits.
func TestAppendReclaimsSidecarOfReusedPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	plantSidecar(t, path+".lock", os.Getpid(), time.Now().Add(-24*time.Hour), "owner-before-reuse")
	if err := AppendLineWithin(path, []byte(`{"q":"ok"}`), 0o644, time.Second); err != nil {
		t.Fatalf("ledger stayed blocked by a reused PID: %v", err)
	}
}

// A dead owner's sidecar is still reclaimed, so recovery is not lost.
func TestAppendReclaimsDeadOwnerSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	plantSidecar(t, path+".lock", deadPID(t), time.Now(), "dead-owner")
	if err := AppendLineWithin(path, []byte(`{"q":"ok"}`), 0o644, time.Second); err != nil {
		t.Fatalf("AppendLineWithin over a dead owner: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("sidecar left behind after append: %v", err)
	}
}

func BenchmarkAppendLine(b *testing.B) {
	path := filepath.Join(b.TempDir(), "events.jsonl")
	body := []byte(`{"kind":"bench","value":12345}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := AppendLine(path, body, 0o600); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAppendLineDurable(b *testing.B) {
	path := filepath.Join(b.TempDir(), "events.jsonl")
	body := []byte(`{"kind":"bench","value":12345}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := AppendLineDurable(path, body, 0o600); err != nil {
			b.Fatal(err)
		}
	}
}
