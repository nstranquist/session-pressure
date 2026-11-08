package sessionpressure

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

func BenchmarkPausedBrowserControlRead(b *testing.B) {
	dir := b.TempDir()
	ctx := context.Background()
	if _, err := BrowserControlStatus(ctx, dir, "paused"); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		state, err := BrowserControlStatus(ctx, dir, "")
		if err != nil || state.Mode != "paused" || !state.Blocked {
			b.Fatalf("paused read: %+v %v", state, err)
		}
	}
}

func TestUnchangedPausedBrowserReadsDoNotReplaceDurableControl(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	initial, err := BrowserControlStatus(ctx, dir, "paused")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "browser-control.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		state, err := BrowserControlStatus(ctx, dir, "")
		if err != nil || state != initial {
			t.Fatalf("cached read changed durable state: %+v %v", state, err)
		}
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("unchanged reads replaced the durable record: %v", err)
	}
	changed, err := BrowserControlStatus(ctx, dir, "auto")
	if err != nil || changed.Mode != "auto" || !changed.Blocked {
		t.Fatalf("mode transition failed: %+v %v", changed, err)
	}
	after, err = os.Stat(path)
	if err != nil || os.SameFile(before, after) {
		t.Fatalf("mode transition was not persisted: %v", err)
	}
}

func TestPausedBrowserSamplingStillServesWaitingWork(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "waiting"}[waiting], func(t *testing.T) {
			// recoverySampleDelay opens a fresh coordinator, so use this process's
			// real identity and clock rather than a fixture's synthetic PID/time.
			coordinator := NewWorkCoordinator(t.TempDir(), defaultWorkLimits(10))
			ctx := context.Background()
			if _, err := BrowserControlStatus(ctx, coordinator.Dir, "paused"); err != nil {
				t.Fatal(err)
			}
			want := 120 * time.Second
			if waiting {
				if err := coordinator.HoldAdmission(ctx, holdTestOperationA, WorkClassExpressTest, WorkAdmissionObservation{
					Red: true, Dimension: "cpu", Reason: "host CPU pressure",
				}); err != nil {
					t.Fatal(err)
				}
				want = 5 * time.Second
			}
			policy := DefaultPolicy(16 * 1024)
			policy.WorkLimits = coordinator.Limits
			if got := recoverySampleDelay(ctx, coordinator.Dir, policy, 120*time.Second); got != want {
				t.Fatalf("delay = %s; want %s", got, want)
			}
			// A critical-pressure policy interval must never be made slower.
			if got := recoverySampleDelay(ctx, coordinator.Dir, policy, 2*time.Second); got != 2*time.Second {
				t.Fatalf("critical delay = %s; want 2s", got)
			}
			state, err := BrowserControlStatus(ctx, coordinator.Dir, "")
			if err != nil || state.Mode != "paused" || !state.Blocked {
				t.Fatalf("manual pause changed: %+v %v", state, err)
			}
		})
	}
}

func TestResidentSleepWakesForNewRecoveryDemand(t *testing.T) {
	for _, mode := range []string{"auto", "waiting-work"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			policy := DefaultPolicy(16 * 1024)
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := BrowserControlStatus(ctx, dir, "paused"); err != nil {
				t.Fatal(err)
			}
			done := make(chan bool, 1)
			go func() { done <- waitForResidentSample(ctx, dir, policy, 120*time.Second) }()
			if mode == "auto" {
				if _, err := BrowserControlStatus(ctx, dir, "auto"); err != nil {
					t.Fatal(err)
				}
			} else {
				coordinator := NewWorkCoordinator(dir, policy.WorkLimits)
				if err := coordinator.HoldAdmission(ctx, holdTestOperationA, WorkClassExpressTest, WorkAdmissionObservation{Red: true, Dimension: "cpu"}); err != nil {
					t.Fatal(err)
				}
			}
			if !<-done {
				t.Fatal("new demand did not wake the normal resident sleep")
			}
		})
	}
}

func TestResidentSleepCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForResidentSample(ctx, t.TempDir(), DefaultPolicy(16*1024), 120*time.Second) {
		t.Fatal("canceled wait requested another sample")
	}
}

func TestRecoveryDemandDoesNotWaitOnWorkMutation(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	coordinator := NewWorkCoordinator(t.TempDir(), policy.WorkLimits)
	ctx := context.Background()
	if _, err := BrowserControlStatus(ctx, coordinator.Dir, "paused"); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.HoldAdmission(ctx, holdTestOperationA, WorkClassExpressTest, WorkAdmissionObservation{Red: true, Dimension: "cpu"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(coordinator.statePath())
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := filelock.Acquire(coordinator.statePath(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if got := recoverySampleDelay(probeCtx, coordinator.Dir, policy, 120*time.Second); got != 5*time.Second {
		t.Fatalf("waiting work was hidden by the mutation lock: delay=%s", got)
	}
	after, err := os.Stat(coordinator.statePath())
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("diagnostic recovery probe replaced work state: %v", err)
	}
}
