package lockfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLockCleansUpMaxAgeStaleLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "resource")
	writeLockInfo(t, path+".lock", lockInfo{
		PID:       os.Getpid(),
		Timestamp: time.Now().Add(-staleMaxAge - time.Second).UnixMilli(),
		Token:     "old",
	})

	unlock, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	unlock()

	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed after unlock, got err=%v", err)
	}
}

func TestLockCleansUpDeadPIDLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "resource")
	writeLockInfo(t, path+".lock", lockInfo{
		PID:       -1,
		Timestamp: time.Now().UnixMilli(),
		Token:     "dead",
	})

	unlock, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	unlock()
}

func TestWithFileLockUnlocksAfterError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "resource")
	want := errors.New("boom")

	err := WithFileLock(path, func() error {
		if _, statErr := os.Stat(path + ".lock"); statErr != nil {
			t.Fatalf("lock file not present during callback: %v", statErr)
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("WithFileLock error = %v, want %v", err, want)
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed after callback, got err=%v", err)
	}
}

func TestLockTimeoutBoundsCrossProcessContention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resource")
	writeLockInfo(t, path+".lock", lockInfo{
		PID:       os.Getpid(),
		Timestamp: time.Now().UnixMilli(),
		Token:     "held-elsewhere",
	})

	started := time.Now()
	_, err := LockTimeout(path, 25*time.Millisecond)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("LockTimeout error = %v, want ErrLocked", err)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("LockTimeout elapsed = %s, want bounded contention", elapsed)
	}
}

func TestLockWaitsForConcurrentProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resource")
	ready := filepath.Join(dir, "ready")
	release := filepath.Join(dir, "release")

	cmd := exec.Command(os.Args[0], "-test.run=TestLockfileChildProcess", "--", path, ready, release)
	cmd.Env = append(os.Environ(), "NDEV_LOCKFILE_CHILD=1")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	childDone := false
	t.Cleanup(func() {
		if childDone {
			return
		}
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	})

	waitForFile(t, ready, time.Second)

	acquired := make(chan error, 1)
	go func() {
		unlock, err := Lock(path)
		if err == nil {
			unlock()
		}
		acquired <- err
	}()

	select {
	case err := <-acquired:
		t.Fatalf("Lock acquired while child still held it: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	if err := os.WriteFile(release, []byte("release\n"), 0o644); err != nil {
		t.Fatalf("write release: %v", err)
	}

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("Lock after child release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lock after child release")
	}

	select {
	case err := <-done:
		childDone = true
		if err != nil {
			t.Fatalf("child failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for child")
	}
}

func TestLockfileChildProcess(t *testing.T) {
	if os.Getenv("NDEV_LOCKFILE_CHILD") != "1" {
		return
	}
	args := os.Args
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || len(args) < sep+4 {
		fmt.Fprintln(os.Stderr, "missing child args")
		os.Exit(2)
	}

	path := args[sep+1]
	ready := args[sep+2]
	release := args[sep+3]

	unlock, err := Lock(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "child lock: %v\n", err)
		os.Exit(2)
	}
	if err := os.WriteFile(ready, []byte("ready\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "child ready: %v\n", err)
		os.Exit(2)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(release); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	unlock()
	os.Exit(0)
}

func writeLockInfo(t *testing.T, path string, info lockInfo) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir lock dir: %v", err)
	}
	body, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal lock: %v", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
