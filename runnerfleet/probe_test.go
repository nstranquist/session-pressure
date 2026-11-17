package runnerfleet

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const probeCommit = "6d3b172def047239f863cc4d9d886aec80dc212f"

func probeHost() Host {
	return Host{Name: "runner-a", User: "builder", State: "active", Platform: "linux/amd64",
		SourceCommit: probeCommit, Weight: 1, HeavySlots: 5, LightSlots: 2}
}

func healthyAnswer() []byte {
	return []byte(`{"healthy":true,"protocol":1,"source_commit":"` + probeCommit + `"}` + "\n" +
		`{"drained":false,"protocol":1,"heavy_slots":5,"light_slots":2,"jobs":[{"state":"running","class":"heavy"}]}` + "\n" +
		"Linux x86_64\n")
}

// exitError returns a real *exec.ExitError with the given status.
func exitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != code {
		t.Fatalf("exit %d fixture: %v", code, err)
	}
	return err
}

type fakeProbe struct {
	mu        sync.Mutex
	calls     int
	deadlines []time.Time
	results   []func(context.Context) ([]byte, error)
}

func (f *fakeProbe) run(ctx context.Context, _ Host) ([]byte, error) {
	f.mu.Lock()
	i := f.calls
	f.calls++
	d, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, d)
	f.mu.Unlock()
	return f.results[min(i, len(f.results)-1)](ctx)
}

func withProbe(t *testing.T, f *fakeProbe) {
	t.Helper()
	oldCmd, oldPause, oldMin := probeCommand, probeRetryPause, probeMinAttempt
	probeCommand, probeRetryPause, probeMinAttempt = f.run, 10*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { probeCommand, probeRetryPause, probeMinAttempt = oldCmd, oldPause, oldMin })
}

func ok(context.Context) ([]byte, error) { return healthyAnswer(), nil }

// The probe uses the caller's remaining time minus a margin, not a fixed
// 12s cap: the selector gives it 27s, so a 13s SSH login under load fits.
func TestProbeUsesCallerBudgetNotFixedCap(t *testing.T) {
	f := &fakeProbe{results: []func(context.Context) ([]byte, error){ok}}
	withProbe(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 27*time.Second)
	defer cancel()
	callerDeadline, _ := ctx.Deadline()
	o := Probe(ctx, probeHost())
	if !o.Healthy || o.Error != "" {
		t.Fatalf("observation = %+v", o)
	}
	if got, want := f.deadlines[0], callerDeadline.Add(-probeCallerMargin); !got.Equal(want) {
		t.Fatalf("attempt deadline %v, want caller deadline minus margin %v", got, want)
	}
	if time.Until(f.deadlines[0]) < 20*time.Second {
		t.Fatalf("attempt budget %v is shorter than a loaded SSH login needs", time.Until(f.deadlines[0]))
	}
}

func TestProbeWithoutCallerDeadlineUsesDefaultBudget(t *testing.T) {
	f := &fakeProbe{results: []func(context.Context) ([]byte, error){ok}}
	withProbe(t, f)
	start := time.Now()
	if o := Probe(context.Background(), probeHost()); !o.Healthy {
		t.Fatalf("observation = %+v", o)
	}
	if b := f.deadlines[0].Sub(start); b < probeDefaultBudget-time.Second || b > probeDefaultBudget+time.Second {
		t.Fatalf("default budget %v, want about %v", b, probeDefaultBudget)
	}
}

// A fast ssh transport failure (exit 255) is retried once within the budget.
func TestProbeRetriesFastSSHTransportFailure(t *testing.T) {
	transport := exitError(t, 255)
	f := &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { return nil, transport }, ok}}
	withProbe(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o := Probe(ctx, probeHost())
	if !o.Healthy || f.calls != 2 {
		t.Fatalf("calls=%d observation=%+v", f.calls, o)
	}
	if o.HeavyJobs != 1 || o.Platform != "linux/amd64" {
		t.Fatalf("parsed observation = %+v", o)
	}
}

// A remote command failure is the host's answer; it is not retried.
func TestProbeDoesNotRetryRemoteFailure(t *testing.T) {
	remote := exitError(t, 1)
	f := &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { return nil, remote }}}
	withProbe(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o := Probe(ctx, probeHost())
	if o.Healthy || f.calls != 1 || !strings.Contains(o.Error, "1 attempt(s)") {
		t.Fatalf("calls=%d observation=%+v", f.calls, o)
	}
}

// Two failed transport attempts stop; the error names both attempts.
func TestProbeStopsAfterMaxAttempts(t *testing.T) {
	transport := exitError(t, 255)
	f := &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { return nil, transport }}}
	withProbe(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o := Probe(ctx, probeHost())
	if o.Healthy || f.calls != probeMaxAttempts || !strings.Contains(o.Error, "2 attempt(s)") {
		t.Fatalf("calls=%d observation=%+v", f.calls, o)
	}
}

// No retry when the remaining budget cannot hold another attempt.
func TestProbeSkipsRetryWithoutBudget(t *testing.T) {
	transport := exitError(t, 255)
	f := &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { return nil, transport }, ok}}
	withProbe(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), probeCallerMargin+30*time.Millisecond)
	defer cancel()
	o := Probe(ctx, probeHost())
	if o.Healthy || f.calls != 1 {
		t.Fatalf("calls=%d observation=%+v", f.calls, o)
	}
}

// A probe that outlives its budget reports the elapsed time, not a bare kill.
func TestProbeTimeoutNamesElapsedTime(t *testing.T) {
	f := &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(ctx context.Context) ([]byte, error) { <-ctx.Done(); return nil, errors.New("signal: killed") }}}
	withProbe(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), probeCallerMargin+100*time.Millisecond)
	defer cancel()
	o := Probe(ctx, probeHost())
	if o.Healthy || f.calls != 1 || !strings.Contains(o.Error, "no answer within") || !strings.Contains(o.Error, "signal: killed") {
		t.Fatalf("calls=%d observation=%+v", f.calls, o)
	}
}

// ObservedAt marks the answer, so a slow probe is not already stale for
// consumers that reject observations older than 30s.
func TestProbeObservedAtMarksAnswer(t *testing.T) {
	f := &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { time.Sleep(80 * time.Millisecond); return healthyAnswer(), nil }}}
	withProbe(t, f)
	start := time.Now()
	o := Probe(context.Background(), probeHost())
	if !o.Healthy || o.ObservedAt.Sub(start) < 80*time.Millisecond {
		t.Fatalf("observed_at %v is before the answer (start %v)", o.ObservedAt, start)
	}
}

func TestProbeRejectsInvalidEndpointWithoutSSH(t *testing.T) {
	f := &fakeProbe{results: []func(context.Context) ([]byte, error){ok}}
	withProbe(t, f)
	h := probeHost()
	h.Name = "bad host;rm"
	o := Probe(context.Background(), h)
	if o.Healthy || o.Error != "invalid endpoint" || f.calls != 0 || o.ObservedAt.IsZero() {
		t.Fatalf("calls=%d observation=%+v", f.calls, o)
	}
}

func TestProbeRecordsSSHTimeAndAttempts(t *testing.T) {
	slow := func(context.Context) ([]byte, error) { time.Sleep(30 * time.Millisecond); return healthyAnswer(), nil }
	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){slow}})
	o := Probe(context.Background(), probeHost())
	if !o.Healthy || o.ProbeAttempts != 1 || o.ProbeMS < 30 || o.ProbeMS > 5000 {
		t.Fatalf("healthy probe: attempts=%d ms=%d err=%q", o.ProbeAttempts, o.ProbeMS, o.Error)
	}

	transport := exitError(t, sshTransportFailure)
	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { return nil, transport }, ok,
	}})
	if o := Probe(context.Background(), probeHost()); !o.Healthy || o.ProbeAttempts != 2 || o.ProbeMS < 1 {
		t.Fatalf("retried probe: attempts=%d ms=%d err=%q", o.ProbeAttempts, o.ProbeMS, o.Error)
	}

	remote := exitError(t, 1)
	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { return nil, remote },
	}})
	if o := Probe(context.Background(), probeHost()); o.Healthy || o.ProbeAttempts != 1 || o.ProbeMS < 1 {
		t.Fatalf("failed probe keeps its timing: attempts=%d ms=%d", o.ProbeAttempts, o.ProbeMS)
	}

	bad := probeHost()
	bad.Name = "bad host;"
	if o := Probe(context.Background(), bad); o.ProbeAttempts != 0 || o.ProbeMS != 0 {
		t.Fatalf("no ssh ran, so no timing: attempts=%d ms=%d", o.ProbeAttempts, o.ProbeMS)
	}
}
