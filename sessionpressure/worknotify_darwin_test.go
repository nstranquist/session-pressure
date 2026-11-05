//go:build darwin

package sessionpressure

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkCapacityNotificationCoalescesAndCloses(t *testing.T) {
	dir := t.TempDir()
	wake, stop := watchWorkCapacity(dir)
	defer stop()
	if wake == nil {
		t.Fatal("native notification unavailable")
	}
	for i := 0; i < 10; i++ {
		notifyWorkCapacity(dir)
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("missed capacity notification")
	}
	stop()
	stop()
	info, err := os.Stat(filepath.Join(dir, workNotifyName))
	if err != nil || info.Size() != 1 {
		t.Fatal(info, err)
	}
}
func TestWorkCapacityWakeRechecksAfterRelease(t *testing.T) {
	c := testWorkCoordinator(t)
	ctx := context.Background()
	lease, _, err := c.Acquire(ctx, WorkClassBenchmarkExclusive)
	if err != nil {
		t.Fatal(err)
	}
	waiter, _, err := c.RegisterWaiter(ctx, WorkClassExpressTest, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{}, 1)
	result := make(chan error, 1)
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	go func() {
		l, _, err := waiter.WaitAcquire(deadline, func(WorkStatus, WorkBlocker) {
			select {
			case blocked <- struct{}{}:
			default:
			}
		})
		if l != nil {
			err = l.Release(ctx)
		}
		result <- err
	}()
	select {
	case <-blocked:
	case <-deadline.Done():
		t.Fatal("not queued")
	}
	start := time.Now()
	if err := lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(start) > time.Second {
			t.Fatal("release waited for fallback")
		}
	case <-deadline.Done():
		t.Fatal("missed release")
	}
}
func TestWorkCapacityNotifyDisabled(t *testing.T) {
	t.Setenv("SESSION_PRESSURE_WORK_NOTIFY", "0")
	wake, stop := watchWorkCapacity(t.TempDir())
	defer stop()
	if wake != nil {
		t.Fatal("rollback switch ignored")
	}
}
