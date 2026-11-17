package sessionpressure

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The production helper releases its lease when SIGTERM cancels its context.
// Keep that behavior in a separate process so CancelWork uses real signals.
func TestCancelWorkCooperativeRelease(t *testing.T) {
	const helperEnv = "PRESSURE_TEST_CANCEL_RELEASE_DIR"
	if dir := os.Getenv(helperEnv); dir != "" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		coordinator := NewWorkCoordinator(dir, DefaultPolicy(16<<10).WorkLimits)
		lease, _, err := coordinator.Acquire(context.Background(), WorkClassExpressTest)
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.BindPID(context.Background(), os.Getpid()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ready"), []byte(lease.Record().OperationID), 0600); err != nil {
			t.Fatal(err)
		}
		select {
		case <-signals:
			if err := lease.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
		case <-time.After(testWatchdog):
			t.Fatal("cancel signal did not arrive")
		}
		return
	}
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestCancelWorkCooperativeRelease$")
	child.Env = append(os.Environ(), helperEnv+"="+dir)
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	defer func() { _ = child.Process.Kill() }()
	var operationID []byte
	deadline := time.Now().Add(testWatchdog)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(dir, "ready"))
		if err == nil {
			operationID = raw
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(operationID) == 0 {
		t.Fatal("helper did not acquire lease")
	}
	coordinator := NewWorkCoordinator(dir, DefaultPolicy(16<<10).WorkLimits)
	result, err := CancelWork(context.Background(), coordinator, string(operationID))
	if err != nil {
		t.Fatal(err)
	}
	if result.Work.Used != 0 {
		t.Fatalf("used=%d after cooperative cancel", result.Work.Used)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper failed to release cooperatively: %v", err)
		}
	case <-time.After(testWatchdog):
		t.Fatal("helper did not exit")
	}
	// Missing operations must still fail initial inspection.
	if _, err := CancelWork(context.Background(), coordinator, string(operationID)); !errors.Is(err, errWorkInspectNotFound) {
		t.Fatalf("cancel missing operation: %v", err)
	}
}
