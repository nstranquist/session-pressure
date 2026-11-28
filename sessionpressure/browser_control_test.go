package sessionpressure

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
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

// A recovered, unblocked control record must stay byte-identical
// across fresh samples, so the resident does not rewrite it every sample.
func TestBrowserControlSteadyStateDoesNotRewrite(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	policy := DefaultPolicy(16)
	evidence := func(at time.Time, cpu float64) *browserControlEvidence {
		return &browserControlEvidence{snapshot: Snapshot{Timestamp: at, HostCPUAvailable: true, HostCPUPercent: cpu, FreePercent: 80, LogicalCPUCount: 10}, policy: policy}
	}
	start := time.Now().Add(-25 * time.Second)
	if _, err := browserControlStatus(ctx, dir, "auto", evidence(start, 100)); err != nil {
		t.Fatal(err)
	}
	for step := 1; step <= 2; step++ {
		if _, err := browserControlStatus(ctx, dir, "", evidence(start.Add(time.Duration(step)*6*time.Second), 10)); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "browser-control.json")
	recovered, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []time.Duration{18 * time.Second, 24 * time.Second} {
		state, err := browserControlStatus(ctx, dir, "", evidence(start.Add(offset), 10))
		if err != nil || state.Blocked {
			t.Fatalf("steady: %+v %v", state, err)
		}
	}
	if steady, err := os.ReadFile(path); err != nil || string(steady) != string(recovered) {
		t.Fatalf("steady state rewrote control record:\n%s\nvs\n%s (%v)", recovered, steady, err)
	}
	// A new block still records its sample time, so the same sample cannot
	// count as recovery.
	blockAt := time.Now()
	state, err := browserControlStatus(ctx, dir, "", evidence(blockAt, 100))
	if err != nil || !state.Blocked || !state.LastSample.Equal(blockAt) {
		t.Fatalf("block: %+v %v", state, err)
	}
	state, err = browserControlStatus(ctx, dir, "", evidence(blockAt, 10))
	if err != nil || state.RecoverySamples != 0 || !state.Blocked {
		t.Fatalf("same sample counted as recovery: %+v %v", state, err)
	}
}

// The resident evidence path must not depend on latest.json or
// policy.json on disk.
func TestBrowserControlResidentEvidenceSkipsDisk(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if _, err := BrowserControlStatus(ctx, dir, "auto"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PolicyPath(dir), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	evidence := &browserControlEvidence{snapshot: Snapshot{Timestamp: time.Now(), HostCPUAvailable: true, HostCPUPercent: 100, FreePercent: 80, LogicalCPUCount: 10}, policy: DefaultPolicy(16)}
	state, err := browserControlStatus(ctx, dir, "", evidence)
	if err != nil || !state.Blocked || state.Reason != "Host pressure blocks browser work" {
		t.Fatalf("resident evidence: %+v %v", state, err)
	}
	if _, err := BrowserControlStatus(ctx, dir, ""); err != nil {
		t.Fatalf("CLI path without latest sample must not read policy: %v", err)
	}
}

// A Monitor embedded outside the resident (profiler, harness, test) must not
// signal live host browsers unless it opts in.
func TestNewMonitorDoesNotEnforceBrowsersByDefault(t *testing.T) {
	monitor := NewMonitor(nil, NewTelemetryStore(t.TempDir()), DefaultPolicy(16))
	if monitor.EnforceBrowsers {
		t.Fatal("NewMonitor enforces browser control by default")
	}
}

// An unchanged steady-state evaluation from resident evidence must
// not take the control lock (lock-file churn on every sample).
func TestBrowserControlSteadyResidentPathTakesNoLock(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	policy := DefaultPolicy(16)
	start := time.Now().Add(-20 * time.Second)
	evidence := func(at time.Time, cpu float64) *browserControlEvidence {
		return &browserControlEvidence{snapshot: Snapshot{Timestamp: at, HostCPUAvailable: true, HostCPUPercent: cpu, FreePercent: 80, LogicalCPUCount: 10}, policy: policy}
	}
	if _, err := browserControlStatus(ctx, dir, "auto", evidence(start, 100)); err != nil {
		t.Fatal(err)
	}
	for step := 1; step <= 2; step++ {
		if _, err := browserControlStatus(ctx, dir, "", evidence(start.Add(time.Duration(step)*6*time.Second), 10)); err != nil {
			t.Fatal(err)
		}
	}
	err := filelock.WithFileLockContext(ctx, filepath.Join(dir, "browser-control"), time.Second, func() error {
		// While the exclusive lock is held, pass an already-canceled context:
		// the unlocked steady path ignores ctx and succeeds, whereas any
		// attempt to acquire the file lock fails immediately with Canceled.
		canceledCtx, cancel := context.WithCancel(ctx)
		cancel()
		state, err := browserControlStatus(canceledCtx, dir, "", evidence(start.Add(18*time.Second), 10))
		if err != nil || state.Blocked {
			t.Fatalf("steady under held lock: %+v %v", state, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
