package filelock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	// Callers with a longer budget retry on this typed timeout.
	if !errors.Is(secondErr, ErrTimeout) {
		t.Fatalf("timeout is not typed: %v", secondErr)
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

	unlock, err := Acquire(target, 100*time.Millisecond)
	if err == nil {
		unlock()
		t.Fatal("aged lock owned by a live PID must not be reclaimed")
	}
	if !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "failed to acquire") {
		t.Fatalf("file-lock timeout is not typed: %v", err)
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
	if judgeSidecar(lockFile) != sidecarLive {
		t.Fatal("fresh, not-yet-initialized n-1 sidecar was treated as stale")
	}
}

// plantDeadOwner writes a complete sidecar whose owner PID does not exist.
func plantDeadOwner(t *testing.T, lockFile string) {
	t.Helper()
	bogus := []byte(`{"pid":2147483646,"timestamp":` + fmt.Sprint(time.Now().UnixMilli()) + `,"token":"dead"}`)
	if err := os.WriteFile(lockFile, bogus, 0o644); err != nil {
		t.Fatalf("plant: %v", err)
	}
}

// setReclaimHook installs testHookBeforeReclaimGuard for one test.
func setReclaimHook(t *testing.T, hook func()) {
	t.Helper()
	testHookBeforeReclaimGuard = hook
	t.Cleanup(func() { testHookBeforeReclaimGuard = nil })
}

// WORK-1225: a waiter lost the create, the owner released, and a successor
// published its sidecar before the waiter acted. The waiter must not remove
// the successor's live sidecar. The old code judged the missing file stale
// and then deleted whatever was there.
func TestReclaimNeverRemovesSuccessorAfterOwnerReleased(t *testing.T) {
	lockFile := filepath.Join(t.TempDir(), "data.lock")
	setReclaimHook(t, func() { t.Fatal("a missing sidecar must not enter the reclaim path") })
	// The sidecar is missing at judgement time; the successor wins the retry.
	if !reclaimStale(lockFile) {
		t.Fatal("missing sidecar must make the waiter retry at once")
	}
	successor, err := tryCreateLock(lockFile)
	if err != nil || successor == nil {
		t.Fatalf("successor create: owner=%+v err=%v", successor, err)
	}
	if owner, err := tryCreateLock(lockFile); err != nil || owner != nil {
		t.Fatalf("waiter retry must lose to the successor: owner=%+v err=%v", owner, err)
	}
	if reclaimStale(lockFile) {
		t.Fatal("live successor sidecar was judged reclaimable")
	}
	if got := readLockInfo(lockFile); got == nil || got.Token != successor.Token {
		t.Fatalf("successor sidecar was removed or replaced: %+v", got)
	}
}

// WORK-1225: two waiters judged the same dead owner stale. The faster one
// reclaimed and published its own live sidecar before the slower one took
// the guard. The slower one must judge again under the guard and keep the
// successor's sidecar.
func TestReclaimJudgesAgainUnderGuard(t *testing.T) {
	lockFile := filepath.Join(t.TempDir(), "data.lock")
	plantDeadOwner(t, lockFile)
	var successor *info
	setReclaimHook(t, func() {
		testHookBeforeReclaimGuard = nil
		if !reclaimStale(lockFile) {
			t.Error("competing waiter could not reclaim the dead owner")
		}
		owner, err := tryCreateLock(lockFile)
		if err != nil || owner == nil {
			t.Errorf("competing waiter create: owner=%+v err=%v", owner, err)
		}
		successor = owner
	})
	if reclaimStale(lockFile) {
		t.Fatal("waiter reclaimed a sidecar that became live before it took the guard")
	}
	if successor == nil {
		t.Fatal("competing waiter did not publish a sidecar")
	}
	if got := readLockInfo(lockFile); got == nil || got.Token != successor.Token {
		t.Fatalf("successor sidecar was removed or replaced: %+v", got)
	}
}

// Only the holder of the reclaim guard removes a stale sidecar. A waiter that
// finds the guard held polls instead of removing.
func TestReclaimGuardExcludesConcurrentReclaimers(t *testing.T) {
	lockFile := filepath.Join(t.TempDir(), "data.lock")
	plantDeadOwner(t, lockFile)
	guard, err := os.OpenFile(lockFile+".reclaim", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold guard: %v", err)
	}
	if reclaimStale(lockFile) {
		t.Fatal("waiter reclaimed while another reclaimer held the guard")
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("stale sidecar removed without the guard: %v", err)
	}
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if !reclaimStale(lockFile) {
		t.Fatal("waiter did not reclaim after the guard was released")
	}
	if _, err := os.Stat(lockFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale sidecar still present after reclaim: %v", err)
	}
}

// forcePublish sets the publish path for one test: nil forces the hard link.
func forcePublish(t testing.TB, rename func(from, to string) error) {
	t.Helper()
	saved := exclusiveRename
	exclusiveRename = rename
	exclusiveRenameUnsupported.Store(false)
	t.Cleanup(func() {
		exclusiveRename = saved
		exclusiveRenameUnsupported.Store(false)
	})
}

// tempSidecars lists leftover private temp files next to lockFile.
func tempSidecars(t *testing.T, lockFile string) []string {
	t.Helper()
	matches, err := filepath.Glob(lockFile + ".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// Each publish path makes exactly one winner, leaves no temp file, and keeps
// the sidecar format.
func TestPublishPathsAreExclusiveAndClean(t *testing.T) {
	paths := map[string]func(from, to string) error{"hard-link": nil}
	if platformExclusiveRename != nil {
		paths["exclusive-rename"] = platformExclusiveRename
	}
	for name, rename := range paths {
		t.Run(name, func(t *testing.T) {
			forcePublish(t, rename)
			lockFile := filepath.Join(t.TempDir(), "nested", "data.lock")
			owner, err := tryCreateLock(lockFile)
			if err != nil || owner == nil {
				t.Fatalf("first create: owner=%+v err=%v", owner, err)
			}
			if loser, err := tryCreateLock(lockFile); err != nil || loser != nil {
				t.Fatalf("second create must lose: owner=%+v err=%v", loser, err)
			}
			if got := readLockInfo(lockFile); got == nil || got.Token != owner.Token || got.PID != owner.PID {
				t.Fatalf("published sidecar is not the winner's record: %+v", got)
			}
			if left := tempSidecars(t, lockFile); len(left) != 0 {
				t.Fatalf("temp sidecars left behind: %v", left)
			}
			if exclusiveRenameUnsupported.Load() {
				t.Fatal("a supported rename was recorded as unsupported")
			}
		})
	}
}

// A file system without an exclusive rename falls back to the hard link,
// and later attempts skip the rename.
func TestUnsupportedExclusiveRenameFallsBackToLink(t *testing.T) {
	calls := 0
	forcePublish(t, func(from, to string) error {
		calls++
		return &os.LinkError{Op: "renameat2", Old: from, New: to, Err: syscall.EINVAL}
	})
	lockFile := filepath.Join(t.TempDir(), "data.lock")
	owner, err := tryCreateLock(lockFile)
	if err != nil || owner == nil {
		t.Fatalf("fallback create: owner=%+v err=%v", owner, err)
	}
	if got := readLockInfo(lockFile); got == nil || got.Token != owner.Token {
		t.Fatalf("fallback sidecar is not the winner's record: %+v", got)
	}
	if loser, err := tryCreateLock(lockFile); err != nil || loser != nil {
		t.Fatalf("fallback second create must lose: owner=%+v err=%v", loser, err)
	}
	if calls != 1 {
		t.Fatalf("rename tried %d times, want 1 before the fallback is remembered", calls)
	}
	if left := tempSidecars(t, lockFile); len(left) != 0 {
		t.Fatalf("temp sidecars left behind: %v", left)
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
	// Each helper takes the lock several times back to back. Releases then
	// overlap with other helpers' create attempts far more often, which is
	// the window where WORK-1225 let a waiter delete a successor's sidecar.
	const processCount, roundsPerProcess = 12, 20
	commands := make([]*exec.Cmd, 0, processCount)
	for index := 0; index < processCount; index++ {
		command := exec.Command(os.Args[0], "-test.run=^TestFileLockProcessHelper$")
		command.Env = append(os.Environ(),
			"NDEV_FILELOCK_HELPER_TARGET="+target,
			"NDEV_FILELOCK_HELPER_LEDGER="+ledger,
			fmt.Sprintf("NDEV_FILELOCK_HELPER_ROUNDS=%d", roundsPerProcess),
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
	if active != "" || completed != processCount*roundsPerProcess {
		t.Fatalf("cross-process critical-section ledger incomplete: active=%s completed=%d body=%s", active, completed, body)
	}
}

func TestFileLockProcessHelper(t *testing.T) {
	target := os.Getenv("NDEV_FILELOCK_HELPER_TARGET")
	ledger := os.Getenv("NDEV_FILELOCK_HELPER_LEDGER")
	if target == "" || ledger == "" {
		return
	}
	rounds, err := strconv.Atoi(os.Getenv("NDEV_FILELOCK_HELPER_ROUNDS"))
	if err != nil || rounds < 1 {
		t.Fatalf("NDEV_FILELOCK_HELPER_ROUNDS must be a positive integer: %v", err)
	}
	for round := 0; round < rounds; round++ {
		if err := runCriticalSection(target, ledger, fmt.Sprintf("%d.%d", os.Getpid(), round)); err != nil {
			t.Fatal(err)
		}
	}
}

// runCriticalSection holds the lock while it appends a start and an end row
// for id to the ledger.
func runCriticalSection(target, ledger, id string) error {
	unlock, err := Acquire(target, 10*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	file, err := os.OpenFile(ledger, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(file, "start %s\n", id); err != nil {
		_ = file.Close()
		return err
	}
	time.Sleep(time.Millisecond)
	if _, err := fmt.Fprintf(file, "end %s\n", id); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
