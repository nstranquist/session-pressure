package filelock

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// WORK-1373: an owner killed inside the lock (or a host restart) leaves its
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
	previous := processStartTime
	processStartTime = fn
	t.Cleanup(func() { processStartTime = previous })
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

func TestPlatformProcessStartTimeReadsRealProcesses(t *testing.T) {
	self, ok := platformProcessStartTime(os.Getpid())
	if !ok {
		t.Skip("process start time is not wired on this platform")
	}
	now := time.Now()
	if self.After(now) || self.Before(now.Add(-24*time.Hour)) {
		t.Fatalf("own start time %v is not a recent past time (now %v)", self, now)
	}
	before := time.Now()
	child := startSleeper(t)
	started, ok := platformProcessStartTime(child.Process.Pid)
	if !ok {
		t.Fatal("child start time unreadable")
	}
	// Linux btime has one-second resolution.
	if started.Before(before.Add(-2*time.Second)) || started.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("child start %v outside its spawn window from %v", started, before)
	}
	if _, ok := platformProcessStartTime(deadPIDForTest(t)); ok {
		t.Fatal("dead PID reported a start time")
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
