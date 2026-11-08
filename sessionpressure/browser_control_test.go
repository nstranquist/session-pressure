package sessionpressure

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBrowserControlFreshDistinctRecovery(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	write := func(at time.Time, cpu float64) {
		t.Helper()
		if err := NewTelemetryStore(dir).WriteLatest(Snapshot{Timestamp: at, HostCPUAvailable: true, HostCPUPercent: cpu, FreePercent: 80, LogicalCPUCount: 10}); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now().Add(-15 * time.Second)
	write(start, 100)
	state, err := BrowserControlStatus(ctx, dir, "auto")
	if err != nil || !state.Blocked {
		t.Fatalf("red: %+v %v", state, err)
	}
	write(start.Add(6*time.Second), 20)
	state, err = BrowserControlStatus(ctx, dir, "")
	if err != nil || state.RecoverySamples != 1 || !state.Blocked {
		t.Fatalf("first: %+v %v", state, err)
	}
	for range 3 {
		state, err = BrowserControlStatus(ctx, dir, "")
		if err != nil || !state.Blocked || state.RecoverySamples != 1 {
			t.Fatalf("cached sample counted: %+v %v", state, err)
		}
	}
	write(start.Add(12*time.Second), 20)
	state, err = BrowserControlStatus(ctx, dir, "")
	if err != nil || state.Blocked {
		t.Fatalf("recovery: %+v %v", state, err)
	}
	state, err = BrowserControlStatus(ctx, dir, "paused")
	if err != nil || !state.Blocked {
		t.Fatal(state, err)
	}
	write(time.Now(), 0)
	state, err = BrowserControlStatus(ctx, dir, "")
	if err != nil || !state.Blocked {
		t.Fatal("manual pause lifted", state, err)
	}
}

func TestBrowserControlMissingAndStaleEvidence(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	state, err := BrowserControlStatus(ctx, dir, "")
	if err != nil || state.Blocked || state.Mode != "" {
		t.Fatal(state, err)
	}
	state, err = BrowserControlStatus(ctx, dir, "auto")
	if err != nil || !state.Blocked {
		t.Fatal(state, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "browser-control.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = BrowserControlStatus(ctx, dir, ""); err == nil {
		t.Fatal("corrupt control failed open")
	}
}

func TestBrowserControlRecoverySamplingDemand(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	policy := DefaultPolicy(16)
	if _, err := BrowserControlStatus(ctx, dir, "auto"); err != nil {
		t.Fatal(err)
	}
	if got := recoverySampleDelay(ctx, dir, policy, 120*time.Second); got != 5*time.Second {
		t.Fatal(got)
	}
}

func TestWorkAdmissionLatchRejectsCachedEvidence(t *testing.T) {
	c := testWorkCoordinator(t)
	now := time.Now().UTC()
	c.Now = func() time.Time { return now }
	observe := func(red bool, sampled time.Time) WorkAdmissionLatch {
		t.Helper()
		state, err := c.ObserveAdmissionLatch(context.Background(), WorkAdmissionObservation{Red: red, Recovered: !red, Dimension: "cpu", SampledAt: sampled})
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	observe(true, now)
	now = now.Add(2 * workAdmissionLatchSampleInterval)
	if !observe(true, now).Latched {
		t.Fatal("red did not latch")
	}
	now = now.Add(2 * workAdmissionLatchSampleInterval)
	recovery := now
	if got := observe(false, recovery); got.RecoverySamples != 1 {
		t.Fatal(got)
	}
	now = now.Add(2 * workAdmissionLatchSampleInterval)
	if got := observe(false, recovery); !got.Latched || got.RecoverySamples != 1 {
		t.Fatal("cached evidence released latch", got)
	}
	if got := observe(false, now); got.Latched {
		t.Fatal("fresh recovery did not release", got)
	}
}
