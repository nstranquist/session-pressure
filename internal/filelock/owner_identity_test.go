package filelock

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// plantOwner writes a complete sidecar for pid with the given timestamp.
func plantOwner(t *testing.T, lockFile string, pid int, at time.Time, token string) {
	t.Helper()
	raw := fmt.Sprintf(`{"pid":%d,"timestamp":%d,"token":%q}`, pid, at.UnixMilli(), token)
	if err := os.WriteFile(lockFile, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

// startSleeper starts a child process that lives for the test.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	return child
}

// An owner killed inside the lock (or a host restart) leaves its
// sidecar. When a new process later gets the same PID, the PID is alive but
// the process started after the sidecar was written, so it cannot be the
// owner. The sidecar must be reclaimed, not kept until that process exits.
func TestSidecarOfReusedPIDIsReclaimed(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data")
	child := startSleeper(t)
	// The recorded owner wrote its sidecar an hour before this PID's process
	// started.
	plantOwner(t, target+".lock", child.Process.Pid, time.Now().Add(-time.Hour), "dead-owner-before-reuse")
	unlock, err := Acquire(target, 2*time.Second)
	if err != nil {
		t.Fatalf("sidecar of a reused PID was not reclaimed: %v", err)
	}
	unlock()
}

// The same live process, recorded after it started, is the owner and keeps
// the lock.
func TestSidecarOfLiveOwnerStartedBeforeTimestampIsKept(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data")
	child := startSleeper(t)
	time.Sleep(20 * time.Millisecond)
	plantOwner(t, target+".lock", child.Process.Pid, time.Now(), "live-owner")
	if unlock, err := Acquire(target, 150*time.Millisecond); err == nil {
		unlock()
		t.Fatal("a live owner that started before its sidecar was preempted")
	}
}

func stubProcessStartTime(t *testing.T, fn func(int) (time.Time, bool)) {
	t.Helper()
	previous := readProcessInfo
	readProcessInfo = func(pid int) (processInfo, bool) {
		started, ok := fn(pid)
		return processInfo{started: started}, ok
	}
	t.Cleanup(func() { readProcessInfo = previous })
}

func TestOwnerAliveUsesStartTimeWithSlack(t *testing.T) {
	recordedAt := time.Now().Add(-time.Hour)
	pid := os.Getpid()
	cases := []struct {
		name    string
		started time.Time
		known   bool
		want    bool
	}{
		{"started long before the record", recordedAt.Add(-24 * time.Hour), true, true},
		{"started inside the slack after the record", recordedAt.Add(ownerStartSlack - time.Second), true, true},
		{"started after the slack: reused PID", recordedAt.Add(ownerStartSlack + time.Second), true, false},
		{"start time unknown keeps a live PID", time.Time{}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubProcessStartTime(t, func(int) (time.Time, bool) { return tc.started, tc.known })
			if got := ownerAlive(pid, recordedAt); got != tc.want {
				t.Fatalf("ownerAlive = %v, want %v", got, tc.want)
			}
		})
	}
	if ownerAlive(deadPIDForTest(t), time.Now()) {
		t.Fatal("a dead PID counted as the owner")
	}
}

// An aged live owner is never preempted, whatever the wall-clock age, as long
// as it started before it wrote the sidecar.
func TestAgedOwnerStartedBeforeRecordIsKept(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data")
	recordedAt := time.Now().Add(-time.Hour)
	stubProcessStartTime(t, func(int) (time.Time, bool) { return recordedAt.Add(-time.Minute), true })
	plantOwner(t, target+".lock", os.Getpid(), recordedAt, "aged-live-owner")
	if unlock, err := Acquire(target, 150*time.Millisecond); err == nil {
		unlock()
		t.Fatal("an aged live owner was preempted")
	}
}

func TestPlatformProcessInfoReadsRealProcesses(t *testing.T) {
	selfInfo, ok := platformProcessInfo(os.Getpid())
	self := selfInfo.started
	if !ok {
		t.Skip("process start time is not wired on this platform")
	}
	now := time.Now()
	if self.After(now) || self.Before(now.Add(-24*time.Hour)) {
		t.Fatalf("own start time %v is not a recent past time (now %v)", self, now)
	}
	before := time.Now()
	child := startSleeper(t)
	childInfo, ok := platformProcessInfo(child.Process.Pid)
	started := childInfo.started
	if !ok || childInfo.zombie {
		t.Fatalf("child unreadable or zombie: %+v %v", childInfo, ok)
	}
	// Linux btime has one-second resolution.
	if started.Before(before.Add(-2*time.Second)) || started.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("child start %v outside its spawn window from %v", started, before)
	}
	if _, ok := platformProcessInfo(deadPIDForTest(t)); ok {
		t.Fatal("dead PID reported a start time")
	}
}

// An owner that exited inside the lock but is not yet reaped by its parent
// is a zombie: kill(pid, 0) still succeeds. It is not the owner.
func TestZombieOwnerIsNotLive(t *testing.T) {
	child := exec.Command("true")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Wait() })
	recordedAt := time.Now()
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, ok := platformProcessInfo(child.Process.Pid)
		if !ok {
			t.Skip("process info is not wired on this platform")
		}
		if info.zombie {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not become a zombie")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !isProcessAlive(child.Process.Pid) {
		t.Fatal("precondition: kill(pid, 0) should still succeed for a zombie")
	}
	if ownerAlive(child.Process.Pid, recordedAt) {
		t.Fatal("a zombie counted as the live owner")
	}
}

// A process that died between creating its temp ownership record and
// publishing it leaves the temp file. The guarded stale path removes it, and
// never the temp file of a live process.
func TestReclaimSweepsTempRecordsOfDeadProcesses(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data")
	lockFile := target + ".lock"
	plantOwner(t, lockFile, deadPIDForTest(t), time.Now(), "dead-owner")
	deadTemp := fmt.Sprintf("%s.tmp-%d-abc", lockFile, deadPIDForTest(t))
	liveTemp := fmt.Sprintf("%s.tmp-%d-def", lockFile, os.Getpid())
	unrelated := lockFile + ".tmp-notapid-x"
	for _, name := range []string{deadTemp, liveTemp, unrelated} {
		if err := os.WriteFile(name, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	unlock, err := Acquire(target, time.Second)
	if err != nil {
		t.Fatalf("dead owner not reclaimed: %v", err)
	}
	unlock()
	if _, err := os.Stat(deadTemp); !os.IsNotExist(err) {
		t.Fatalf("temp record of a dead process kept: %v", err)
	}
	for _, name := range []string{liveTemp, unrelated} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("%s removed: %v", filepath.Base(name), err)
		}
	}
}

func deadPIDForTest(t *testing.T) int {
	t.Helper()
	for pid := 4_000_000; pid > 3_000_000; pid -= 7919 {
		if !isProcessAlive(pid) {
			return pid
		}
	}
	t.Skip("no free PID found")
	return 0
}

// A nicos-tools breaker (filelock.BreakStale there) locks the
// stale sidecar's own inode, not the guard file. While it holds that lock, a
// waiter here must not remove the sidecar: it could be past the breaker's
// checks and about to publish a successor.
func TestReclaimWaitsForForeignInodeBreaker(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data")
	lockFile := target + ".lock"
	plantOwner(t, lockFile, deadPIDForTest(t), time.Now(), "dead-owner")
	foreign, err := os.Open(lockFile)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err := syscall.Flock(int(foreign.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if reclaimStale(lockFile) {
		t.Fatal("reclaim went ahead while a foreign breaker held the inode lock")
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("sidecar removed under a foreign breaker: %v", err)
	}
	if err := syscall.Flock(int(foreign.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if !reclaimStale(lockFile) {
		t.Fatal("dead owner not reclaimed after the foreign breaker let go")
	}
}

// A breaker that locked an inode which another breaker then removed and
// replaced must leave the new file alone, even if it also looks stale: this
// waiter never judged it under its locks.
func TestReclaimLeavesSidecarReplacedAfterLocks(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data")
	lockFile := target + ".lock"
	plantOwner(t, lockFile, deadPIDForTest(t), time.Now(), "dead-owner")
	previous := testHookAfterReclaimLocks
	testHookAfterReclaimLocks = func() {
		// A foreign breaker (no guard file) removes the locked inode, and a
		// successor publishes. plantOwner writes a new inode.
		_ = os.Remove(lockFile)
		plantOwner(t, lockFile, os.Getpid(), time.Now(), successorTokenForTest)
	}
	t.Cleanup(func() { testHookAfterReclaimLocks = previous })
	if reclaimStale(lockFile) {
		t.Fatal("reclaim reported success after the path changed inode")
	}
	raw, err := os.ReadFile(lockFile)
	if err != nil || !strings.Contains(string(raw), successorTokenForTest) {
		t.Fatalf("successor sidecar removed or replaced: %q %v", raw, err)
	}
}

const successorTokenForTest = "successor-after-foreign-break"

// TestReclaimUnderCrashingOwnersAcrossProcesses makes the dead-owner path
// hot: helpers exit inside the lock without releasing it, so waiters reclaim
// dead sidecars over and over while others contend. A ledger checks that no
// two live holders overlapped.
func TestReclaimUnderCrashingOwnersAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")
	ledger := filepath.Join(dir, "ledger.log")
	const processCount, rounds, dieEvery = 8, 25, 7
	commands := make([]*exec.Cmd, 0, processCount)
	for index := 0; index < processCount; index++ {
		command := exec.Command(os.Args[0], "-test.run=^TestCrashingOwnerHelper$")
		command.Env = append(os.Environ(),
			"FILELOCK_CRASH_TARGET="+target,
			"FILELOCK_CRASH_LEDGER="+ledger,
			fmt.Sprintf("FILELOCK_CRASH_ROUNDS=%d", rounds),
			fmt.Sprintf("FILELOCK_CRASH_DIE_EVERY=%d", dieEvery+index),
		)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
				t.Fatalf("helper %d: %v", index, err)
			}
		}
	}
	body, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	active, starts, deaths := "", 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("bad ledger line %q", line)
		}
		switch fields[0] {
		case "start":
			if active != "" {
				t.Fatalf("holders overlapped: %s then %s", active, fields[1])
			}
			active = fields[1]
			starts++
		case "end", "die":
			if active != fields[1] {
				t.Fatalf("ledger drift: active=%s %s=%s", active, fields[0], fields[1])
			}
			active = ""
			if fields[0] == "die" {
				deaths++
			}
		}
	}
	if deaths == 0 {
		t.Fatal("no helper died inside the lock; the reclaim path was not exercised")
	}
	t.Logf("starts=%d deaths=%d (each death left a sidecar that a waiter reclaimed)", starts, deaths)
}

func TestCrashingOwnerHelper(t *testing.T) {
	target := os.Getenv("FILELOCK_CRASH_TARGET")
	ledger := os.Getenv("FILELOCK_CRASH_LEDGER")
	if target == "" || ledger == "" {
		return
	}
	var rounds, dieEvery int
	_, _ = fmt.Sscan(os.Getenv("FILELOCK_CRASH_ROUNDS"), &rounds)
	_, _ = fmt.Sscan(os.Getenv("FILELOCK_CRASH_DIE_EVERY"), &dieEvery)
	file, err := os.OpenFile(ledger, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= rounds; round++ {
		unlock, err := Acquire(target, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("%d.%d", os.Getpid(), round)
		_, _ = fmt.Fprintf(file, "start %s\n", id)
		time.Sleep(200 * time.Microsecond)
		if round%dieEvery == 0 {
			// Die holding the lock: the sidecar stays with a dead PID.
			_, _ = fmt.Fprintf(file, "die %s\n", id)
			os.Exit(3)
		}
		_, _ = fmt.Fprintf(file, "end %s\n", id)
		unlock()
	}
}
