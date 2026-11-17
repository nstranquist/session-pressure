package runnerfleet

import (
	"context"
	"encoding/json"
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

func withSlowAfter(t *testing.T, d time.Duration) {
	t.Helper()
	old := probeSlowAfter
	probeSlowAfter = d
	t.Cleanup(func() { probeSlowAfter = old })
}

// A prompt answer reports transport ok with the SSH time.
func TestProbeTransportOK(t *testing.T) {
	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){ok}})
	o := Probe(context.Background(), probeHost())
	if !o.Healthy || o.Transport != TransportOK || o.ProbeMS < 1 {
		t.Fatalf("observation = %+v", o)
	}
	body, _ := json.Marshal(o)
	if !strings.Contains(string(body), `"transport":"ok"`) || !strings.Contains(string(body), `"probe_ms":`) {
		t.Fatalf("JSON lacks transport or probe_ms: %s", body)
	}
}

// A late answer keeps the scheduler's health answer and reports slow. It is
// not marked unhealthy, and an unhealthy scheduler stays unhealthy.
func TestProbeTransportSlowKeepsSchedulerHealth(t *testing.T) {
	withSlowAfter(t, 40*time.Millisecond)
	late := func(answer []byte) func(context.Context) ([]byte, error) {
		return func(context.Context) ([]byte, error) { time.Sleep(60 * time.Millisecond); return answer, nil }
	}
	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){late(healthyAnswer())}})
	o := Probe(context.Background(), probeHost())
	if !o.Healthy || o.Error != "" || o.Transport != TransportSlow || o.ProbeMS < 60 {
		t.Fatalf("late healthy answer: %+v", o)
	}

	sick := []byte(strings.Replace(string(healthyAnswer()), `"healthy":true`, `"healthy":false`, 1))
	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){late(sick)}})
	o = Probe(context.Background(), probeHost())
	if o.Healthy || o.Transport != TransportSlow || !strings.Contains(o.Error, "unhealthy scheduler") {
		t.Fatalf("late unhealthy answer: %+v", o)
	}

	// An answer that needed the retry after an ssh failure is slow too.
	withSlowAfter(t, time.Hour)
	transport := exitError(t, sshTransportFailure)
	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { return nil, transport }, ok}})
	if o := Probe(context.Background(), probeHost()); !o.Healthy || o.Transport != TransportSlow || o.ProbeAttempts != 2 {
		t.Fatalf("retried answer: %+v", o)
	}
}

// No answer (budget spent, or ssh's own failure on every attempt) reports
// unreachable. A remote command failure is the host's answer, not unreachable.
func TestProbeTransportUnreachable(t *testing.T) {
	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(ctx context.Context) ([]byte, error) { <-ctx.Done(); return nil, errors.New("signal: killed") }}})
	ctx, cancel := context.WithTimeout(context.Background(), probeCallerMargin+100*time.Millisecond)
	defer cancel()
	o := Probe(ctx, probeHost())
	if o.Healthy || o.Transport != TransportUnreachable || o.ProbeMS < 50 || !strings.Contains(o.Error, "no answer within") {
		t.Fatalf("timed-out probe: %+v", o)
	}

	transport := exitError(t, sshTransportFailure)
	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { return nil, transport }}})
	if o := Probe(context.Background(), probeHost()); o.Transport != TransportUnreachable || o.ProbeAttempts != probeMaxAttempts {
		t.Fatalf("ssh failure on every attempt: %+v", o)
	}

	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { return nil, &exec.Error{Name: "ssh", Err: exec.ErrNotFound} }}})
	if o := Probe(context.Background(), probeHost()); o.Transport != TransportUnreachable {
		t.Fatalf("ssh did not start: %+v", o)
	}

	remote := exitError(t, 1)
	withProbe(t, &fakeProbe{results: []func(context.Context) ([]byte, error){
		func(context.Context) ([]byte, error) { return nil, remote }}})
	if o := Probe(context.Background(), probeHost()); o.Healthy || o.Transport != TransportOK {
		t.Fatalf("remote failure is an answer: %+v", o)
	}

	bad := probeHost()
	bad.Name = "bad host;"
	if o := Probe(context.Background(), bad); o.Transport != "" {
		t.Fatalf("no probe ran, so no transport state: %+v", o)
	}
}

// Selection refuses an unreachable host and names the transport reason; a
// slow but healthy host stays selectable.
func TestAssignRefusesUnreachableTransport(t *testing.T) {
	path, _ := fixture(t)
	probe := func(_ context.Context, h Host) Observation {
		return Observation{Host: h, Transport: TransportUnreachable, ProbeMS: 25000,
			Error: "SSH health probe: no answer within 25.0s: signal: killed (1 attempt(s), 25.0s)"}
	}
	_, err := Assign(context.Background(), path, req("unreachable"), probe)
	if err == nil || !strings.Contains(err.Error(), "runner-2: transport unreachable after 25000ms") ||
		!strings.Contains(err.Error(), "runner-3: transport unreachable") || !strings.Contains(err.Error(), "request remains unassigned") {
		t.Fatalf("err = %v", err)
	}

	slow := func(_ context.Context, h Host) Observation {
		o := healthy(context.Background(), h)
		o.Transport, o.ProbeMS = TransportSlow, 9000
		if h.Name == "runner-2" {
			o.Healthy, o.Transport, o.Error = false, TransportUnreachable, "no answer"
		}
		return o
	}
	b, err := Assign(context.Background(), path, req("slow-ok"), slow)
	if err != nil || b.Host.Name != "runner-3" {
		t.Fatalf("slow healthy host must stay selectable: %+v %v", b, err)
	}
}
