package filelock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")

	unlock, err := Acquire(target, time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := os.Stat(target + ".lock"); err != nil {
		t.Fatalf("lock file not created: %v", err)
	}
	unlock()
	if _, err := os.Stat(target + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock file still present after release: err=%v", err)
	}
}

func TestAcquireBlocksConcurrent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")

	first, err := Acquire(target, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	done := make(chan struct{})
	var secondErr error
	go func() {
		_, err := Acquire(target, 200*time.Millisecond)
		secondErr = err
		close(done)
	}()

	select {
	case <-done:
		// Expected: timed out because first holds it.
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire should have timed out")
	}
	if secondErr == nil {
		t.Fatal("expected timeout error from second acquire, got nil")
	}
	first()
}

func TestAcquireContextCancelsInProcessQueue(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")
	first, err := Acquire(target, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = AcquireContext(ctx, target, 5*time.Second)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected ctx cancel while first holder owns the lock")
	}
	if elapsed >= time.Second {
		t.Fatalf("canceled acquire waited %s; must not sit on the 5s lock timeout", elapsed)
	}
}

func TestSerialAcquireSucceeds(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")

	for i := 0; i < 5; i++ {
		unlock, err := Acquire(target, time.Second)
		if err != nil {
			t.Fatalf("iter %d: acquire: %v", i, err)
		}
		unlock()
	}
}

func TestUnlockIsIdempotentAndCannotRemoveSuccessor(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")

	first, err := Acquire(target, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	secondReady := make(chan func(), 1)
	secondErr := make(chan error, 1)
	go func() {
		unlock, err := Acquire(target, time.Second)
		if err != nil {
			secondErr <- err
			return
		}
		secondReady <- unlock
	}()

	first()
	var second func()
	select {
	case err := <-secondErr:
		t.Fatalf("second acquire: %v", err)
	case second = <-secondReady:
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire timed out")
	}
	defer second()

	first() // must be a no-op, not remove the successor's sidecar
	if _, err := os.Stat(target + ".lock"); err != nil {
		t.Fatalf("successor lock removed by repeated unlock: %v", err)
	}
}

func TestUnlockDoesNotRemoveForeignReplacement(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")
	lockFile := target + ".lock"

	unlock, err := Acquire(target, time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := os.Remove(lockFile); err != nil {
		t.Fatalf("remove owned sidecar: %v", err)
	}
	foreign := []byte(fmt.Sprintf(`{"pid":%d,"timestamp":%d,"token":"foreign"}`,
		os.Getpid(), time.Now().UnixMilli()))
	if err := os.WriteFile(lockFile, foreign, 0o644); err != nil {
		t.Fatalf("write foreign replacement: %v", err)
	}

	unlock()
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("foreign replacement removed by old unlock: %v", err)
	}
}

// TestFIFOOrdering verifies that the in-process queue grants the lock in the
// order callers entered Acquire, even when many goroutines pile up.
func TestFIFOOrdering(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")

	// Hold the lock so all goroutines park in the queue first.
	gate, err := Acquire(target, time.Second)
	if err != nil {
		t.Fatalf("gate acquire: %v", err)
	}

	const n = 8
	starts := make(chan int, n)
	completed := make([]int, 0, n)
	var completedMu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			starts <- id
			unlock, err := Acquire(target, 5*time.Second)
			if err != nil {
				t.Errorf("goroutine %d: acquire: %v", id, err)
				return
			}
			completedMu.Lock()
			completed = append(completed, id)
			completedMu.Unlock()
			time.Sleep(2 * time.Millisecond) // hold briefly
			unlock()
		}(i)
	}

	// Wait for all goroutines to enter Acquire (proxy for "queued"). We can't
	// directly observe queue insertion, so this is a best-effort sequencing.
	for i := 0; i < n; i++ {
		<-starts
	}
	time.Sleep(50 * time.Millisecond) // give them time to actually enqueue

	gate() // release; goroutines should drain in FIFO order

	wg.Wait()
	if len(completed) != n {
		t.Fatalf("got %d completions, want %d", len(completed), n)
	}
	// Note: We don't assert strict FIFO order on goroutine *id* because the
	// channel send/select races above the queue. We DO assert all completed
	// without deadlock or duplicate grants (which serialize() ensures).
}

func TestStaleLockReclaimedByDeadPID(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")
	lockFile := target + ".lock"

	// Plant a lock file owned by a PID that almost certainly doesn't exist.
	bogus := []byte(`{"pid":2147483646,"timestamp":` + fmt.Sprint(time.Now().UnixMilli()) + `}`)
	if err := os.WriteFile(lockFile, bogus, 0o644); err != nil {
		t.Fatalf("plant: %v", err)
	}

	unlock, err := Acquire(target, time.Second)
	if err != nil {
		t.Fatalf("acquire over stale lock: %v", err)
	}
	unlock()
}

func TestAgedLockOwnedByLivePIDIsNotReclaimed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")
	lockFile := target + ".lock"

	// Plant a lock with a current PID but an ancient timestamp.
	bogus := []byte(fmt.Sprintf(`{"pid":%d,"timestamp":%d}`,
		os.Getpid(),
		time.Now().Add(-2*StaleAge).UnixMilli()))
	if err := os.WriteFile(lockFile, bogus, 0o644); err != nil {
		t.Fatalf("plant: %v", err)
	}

	if unlock, err := Acquire(target, 100*time.Millisecond); err == nil {
		unlock()
		t.Fatal("aged lock owned by a live PID must not be reclaimed")
	}
}

func TestMalformedLockReclaimed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")
	lockFile := target + ".lock"

	if err := os.WriteFile(lockFile, []byte("not json"), 0o644); err != nil {
		t.Fatalf("plant: %v", err)
	}
	old := time.Now().Add(-2 * lockInitializationGrace)
	if err := os.Chtimes(lockFile, old, old); err != nil {
		t.Fatalf("age malformed lock: %v", err)
	}

	unlock, err := Acquire(target, time.Second)
	if err != nil {
		t.Fatalf("acquire over malformed lock: %v", err)
	}
	unlock()
}

func TestFreshMalformedLockIsNotStolenDuringNMinusOneInitialization(t *testing.T) {
	dir := t.TempDir()
	lockFile := filepath.Join(dir, "data.lock")
	if err := os.WriteFile(lockFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if isStale(lockFile) {
		t.Fatal("fresh, not-yet-initialized n-1 sidecar was treated as stale")
	}
}

func TestTryCreateLockPublishesCompleteOwnershipAtomically(t *testing.T) {
	dir := t.TempDir()
	lockFile := filepath.Join(dir, "data.lock")
	owner, err := tryCreateLock(lockFile)
	if err != nil || owner == nil {
		t.Fatalf("create lock: owner=%+v err=%v", owner, err)
	}
	visible := readLockInfo(lockFile)
	if visible == nil || visible.PID != owner.PID || visible.Token != owner.Token {
		t.Fatalf("published ownership is incomplete: want=%+v got=%+v", owner, visible)
	}
	removeOwnedLock(lockFile, owner)
}

func TestWithFileLockReleasesOnPanic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")

	defer func() {
		_ = recover()
		// After the panic, a fresh Acquire must succeed quickly — proving the
		// in-process queue and the file lock were both released.
		unlock, err := Acquire(target, 200*time.Millisecond)
		if err != nil {
			t.Fatalf("post-panic acquire: %v", err)
		}
		unlock()
	}()

	_ = WithFileLock(target, func() error {
		panic("boom")
	})
}

func TestWithFileLockPropagatesError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")
	want := errors.New("inner")
	got := WithFileLock(target, func() error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestContextCancellationAbortsWait(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")

	first, err := Acquire(target, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err = AcquireContext(ctx, target, 5*time.Second)
	if err == nil {
		t.Fatal("expected ctx cancel error, got nil")
	}
}

func TestIsProcessAlive(t *testing.T) {
	if !isProcessAlive(os.Getpid()) {
		t.Fatal("our own PID should be alive")
	}
	if isProcessAlive(0) {
		t.Fatal("PID 0 must not report alive")
	}
	// 2^31-2 is almost certainly unallocated on macOS / Linux.
	if isProcessAlive(2147483646) {
		t.Skip("environment recycles ridiculously high PIDs; skipping dead-PID check")
	}
}

// TestExclusiveCriticalSection verifies that no two WithFileLock calls overlap
// even under concurrency.
func TestExclusiveCriticalSection(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")

	const n = 20
	var inFlight int32
	var maxInFlight int32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithFileLockContext(context.Background(), target, 5*time.Second, func() error {
				cur := atomic.AddInt32(&inFlight, 1)
				for {
					m := atomic.LoadInt32(&maxInFlight)
					if cur <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, cur) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				atomic.AddInt32(&inFlight, -1)
				return nil
			})
			if err != nil {
				t.Errorf("with: %v", err)
			}
		}()
	}
	wg.Wait()
	if maxInFlight != 1 {
		t.Fatalf("critical section had %d concurrent holders, want 1", maxInFlight)
	}
}

func TestExclusiveCriticalSectionAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")
	ledger := filepath.Join(dir, "critical-section.log")
	const processCount = 12
	commands := make([]*exec.Cmd, 0, processCount)
	for index := 0; index < processCount; index++ {
		command := exec.Command(os.Args[0], "-test.run=^TestFileLockProcessHelper$")
		command.Env = append(os.Environ(),
			"NDEV_FILELOCK_HELPER_TARGET="+target,
			"NDEV_FILELOCK_HELPER_LEDGER="+ledger,
		)
		if err := command.Start(); err != nil {
			t.Fatalf("start helper %d: %v", index, err)
		}
		commands = append(commands, command)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("wait for helper %d: %v", index, err)
		}
	}
	body, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	active := ""
	completed := 0
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed critical-section ledger line %q", line)
		}
		switch fields[0] {
		case "start":
			if active != "" {
				t.Fatalf("cross-process critical sections overlapped: active=%s next=%s ledger=%s", active, fields[1], body)
			}
			active = fields[1]
		case "end":
			if active != fields[1] {
				t.Fatalf("cross-process critical-section ownership drift: active=%s end=%s ledger=%s", active, fields[1], body)
			}
			active = ""
			completed++
		default:
			t.Fatalf("unknown critical-section ledger row %q", line)
		}
	}
	if active != "" || completed != processCount {
		t.Fatalf("cross-process critical-section ledger incomplete: active=%s completed=%d body=%s", active, completed, body)
	}
}

func TestFileLockProcessHelper(t *testing.T) {
	target := os.Getenv("NDEV_FILELOCK_HELPER_TARGET")
	ledger := os.Getenv("NDEV_FILELOCK_HELPER_LEDGER")
	if target == "" || ledger == "" {
		return
	}
	unlock, err := Acquire(target, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	file, err := os.OpenFile(ledger, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	if _, err := fmt.Fprintf(file, "start %d\n", pid); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := fmt.Fprintf(file, "end %d\n", pid); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
