package sessionpressure

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func freshHeadroom(now time.Time, headroomMB int64) MemoryHeadroom {
	return MemoryHeadroom{SampleFresh: true, SampledAt: now.Add(-10 * time.Second), HeadroomMB: headroomMB}
}

func testBrowserAdmitter(t *testing.T, dir string, pid int, headroom func() MemoryHeadroom) *browserAdmitter {
	t.Helper()
	return &browserAdmitter{
		dir: dir, runtime: BrowserRuntimeHeadlessShell, cost: browserHeadlessShellSessionMB, pid: pid,
		now:   time.Now,
		alive: func(int) bool { return true },
		headroom: func(context.Context, time.Time) (MemoryHeadroom, error) {
			return headroom(), nil
		},
	}
}

func TestBrowserCapacityDerivesSlotsFromHeadroomAndRuntime(t *testing.T) {
	now := time.Now()
	headroom := freshHeadroom(now, 23_000)
	shell := decideBrowserCapacity(headroom, nil, "headless-shell", browserSessionCostMB("headless-shell", 70))
	if !shell.Admit || shell.CostMB != 290 || shell.Slots != 79 {
		t.Fatalf("headless-shell = %+v, want admit with 79 slots of 290 MB", shell)
	}
	cft := decideBrowserCapacity(headroom, nil, "chrome-for-testing", browserSessionCostMB("chrome-for-testing", 70))
	if !cft.Admit || cft.CostMB != 928 || cft.Slots != 24 {
		t.Fatalf("cft = %+v, want admit with 24 slots of 928 MB", cft)
	}
	// Unknown runtimes take the conservative Chrome for Testing cost, and a
	// command whose own measured tree is larger is charged that instead.
	if got := browserSessionCostMB("", 0); got != browserChromeForTestingSessionMB {
		t.Fatalf("unknown runtime cost = %d", got)
	}
	if got := browserSessionCostMB("headless-shell", 1500); got != 1500 {
		t.Fatalf("measured tree cost = %d, want 1500", got)
	}
}

func TestBrowserCapacityChargesAdmissionsNewerThanSample(t *testing.T) {
	now := time.Now()
	headroom := freshHeadroom(now, 700)
	entries := []BrowserAdmissionEntry{
		// Started after the sample: not in it yet.
		{PID: 1, CostMB: 290, At: now.Add(-3 * time.Second), StartedAt: now.Add(-2 * time.Second)},
		// Started well before the sample: already in available memory.
		{PID: 2, CostMB: 290, At: now.Add(-2 * time.Minute), StartedAt: now.Add(-110 * time.Second)},
		// Still queued for a lease: its browser has not started, so charge it.
		{PID: 3, CostMB: 290, At: now.Add(-time.Minute)},
	}
	decision := decideBrowserCapacity(headroom, entries, "headless-shell", 290)
	if decision.ChargedMB != 580 || decision.Active != 3 || decision.AvailableMB != 120 || decision.Admit || decision.Slots != 3 {
		t.Fatalf("decision = %+v, want 580 MB charged, 3 active, refused", decision)
	}
	if !strings.HasPrefix(decision.Reason, "no_headroom") {
		t.Fatalf("reason = %q", decision.Reason)
	}
}

func TestBrowserCapacityWithoutFreshSampleAdmitsOneAtATime(t *testing.T) {
	stale := MemoryHeadroom{SampleFresh: false}
	if got := decideBrowserCapacity(stale, nil, "headless-shell", 290); !got.Admit || got.Slots != 1 {
		t.Fatalf("idle host without sample = %+v, want one slot", got)
	}
	busy := []BrowserAdmissionEntry{{PID: 1, CostMB: 290, At: time.Now()}}
	if got := decideBrowserCapacity(stale, busy, "headless-shell", 290); got.Admit {
		t.Fatalf("second browser without sample admitted: %+v", got)
	}
}

func TestPruneBrowserAdmissionsReleasesDeadHoldersAndDropsCoveredRows(t *testing.T) {
	now := time.Now()
	entries := []BrowserAdmissionEntry{
		{PID: 10, At: now.Add(-time.Second)},                                            // live
		{PID: 11, At: now.Add(-time.Second)},                                            // dead, recent: keep charge
		{PID: 12, At: now.Add(-10 * time.Minute), Released: true},                       // covered: drop
		{PID: 13, At: now.Add(-10 * time.Minute), StartedAt: now.Add(-9 * time.Minute)}, // dead and old: drop
	}
	kept := pruneBrowserAdmissions(entries, now, func(pid int) bool { return pid == 10 })
	if len(kept) != 2 || kept[0].Released || !kept[1].Released || kept[1].PID != 11 {
		t.Fatalf("kept = %+v", kept)
	}
}

func TestBrowserAdmissionLedgerSharesHeadroomAcrossCommands(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// Room for exactly one headless-shell session.
	headroom := func() MemoryHeadroom { return freshHeadroom(now, 400) }
	first, err := testBrowserAdmitter(t, dir, 101, headroom).tryAcquireOnce(t)
	if err != nil || first == nil {
		t.Fatalf("first admission = %v, %v", first, err)
	}
	second, err := testBrowserAdmitter(t, dir, 102, headroom).tryAcquireOnce(t)
	if err != nil || second != nil {
		t.Fatalf("second admission should wait for headroom: %v, %v", second, err)
	}
	// More memory (for example, a newer sample after a build finished)
	// admits the second command while the first still runs.
	more := func() MemoryHeadroom { return freshHeadroom(now, 2_000) }
	third, err := testBrowserAdmitter(t, dir, 103, more).tryAcquireOnce(t)
	if err != nil || third == nil {
		t.Fatalf("third admission = %v, %v", third, err)
	}
	if third.Decision.Active != 1 || third.Decision.ChargedMB != 290 {
		t.Fatalf("third decision = %+v, want one active and its 290 MB charged", third.Decision)
	}
	first.Started()
	first.Release()
	entries, err := readBrowserAdmissions(dir + "/" + browserAdmissionLedgerName)
	if err != nil || len(entries) != 2 || !entries[0].Released || entries[0].StartedAt.IsZero() {
		t.Fatalf("ledger after release = %+v, %v", entries, err)
	}
}

func (admitter *browserAdmitter) tryAcquireOnce(t *testing.T) (*browserAdmission, error) {
	t.Helper()
	_, admission, err := admitter.tryAcquire(context.Background())
	return admission, err
}

func TestWaitForBrowserAdmissionWaitsThroughPauseAndMemory(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	var controlCalls, headroomCalls atomic.Int32
	control := func(context.Context) error {
		if controlCalls.Add(1) <= 2 {
			return errors.New("BROWSER_PAUSED: Host pressure blocks browser work; inspect: ndev session pressure browser status")
		}
		return nil
	}
	admitter := testBrowserAdmitter(t, dir, 201, func() MemoryHeadroom {
		if headroomCalls.Add(1) <= 2 {
			return freshHeadroom(now, 100)
		}
		return freshHeadroom(now, 4_000)
	})
	var stderr bytes.Buffer
	reporter := newWorkProgressReporter(WorkProgressHuman, &stderr, "op", WorkClassBrowser, 3, time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), testWatchdog)
	defer cancel()
	admission, err := waitForBrowserAdmission(ctx, true, control, admitter, reporter, 5*time.Millisecond)
	if err != nil || admission == nil {
		t.Fatalf("wait = %v, %v", admission, err)
	}
	defer admission.Release()
	if controlCalls.Load() != 5 || headroomCalls.Load() != 3 {
		t.Fatalf("control calls %d, headroom calls %d", controlCalls.Load(), headroomCalls.Load())
	}
	out := stderr.String()
	if !strings.Contains(out, "blocker=browser_paused") || !strings.Contains(out, "blocker=browser_memory") || !strings.Contains(out, "BROWSER_NO_HEADROOM") {
		t.Fatalf("progress lines missing a blocker:\n%s", out)
	}
}

func TestWaitForBrowserAdmissionRefusesAtOnceWithoutWait(t *testing.T) {
	admitter := testBrowserAdmitter(t, t.TempDir(), 301, func() MemoryHeadroom { return freshHeadroom(time.Now(), 4_000) })
	paused := func(context.Context) error { return errors.New("BROWSER_PAUSED: paused by the operator") }
	var stderr bytes.Buffer
	reporter := newWorkProgressReporter(WorkProgressHuman, &stderr, "op", WorkClassBrowser, 3, time.Now())
	_, err := waitForBrowserAdmission(context.Background(), false, paused, admitter, reporter, time.Millisecond)
	if err == nil || !strings.HasPrefix(err.Error(), "BROWSER_PAUSED") || stderr.Len() != 0 {
		t.Fatalf("wait 0 = %v, stderr %q", err, stderr.String())
	}
}

func TestWaitForBrowserAdmissionStopsAtDeadline(t *testing.T) {
	admitter := testBrowserAdmitter(t, t.TempDir(), 401, func() MemoryHeadroom { return freshHeadroom(time.Now(), 10) })
	ok := func(context.Context) error { return nil }
	// The budget ends at the first refusal, not after a wall-clock 60ms: on a
	// loaded host the first lock wait alone can outlast a short timeout. The
	// long retry keeps the timer from starting another attempt on a spent
	// context.
	deadline := newManualDeadline()
	stderr := &expireOnWrite{deadline: deadline, marker: "ndev session pressure: waiting"}
	reporter := newWorkProgressReporter(WorkProgressHuman, stderr, "op", WorkClassBrowser, 3, time.Now())
	_, err := waitForBrowserAdmission(deadline, true, ok, admitter, reporter, testWatchdog)
	if err == nil || !strings.Contains(err.Error(), "deadline reached") || !strings.Contains(err.Error(), "BROWSER_NO_HEADROOM") {
		t.Fatalf("deadline err = %v (progress %q)", err, stderr.String())
	}
}

// expireOnWrite ends a manual deadline when the progress stream first shows
// the marker, so a test can end a wait budget at an exact stage.
type expireOnWrite struct {
	bytes.Buffer
	deadline *manualDeadline
	marker   string
}

func (w *expireOnWrite) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if strings.Contains(w.Buffer.String(), w.marker) {
		w.deadline.expire()
	}
	return n, err
}

func TestWaitForBrowserAdmissionReturnsControlErrors(t *testing.T) {
	admitter := testBrowserAdmitter(t, t.TempDir(), 501, func() MemoryHeadroom { return freshHeadroom(time.Now(), 4_000) })
	broken := func(context.Context) error { return errors.New("invalid browser control: torn") }
	_, err := waitForBrowserAdmission(context.Background(), true, broken, admitter, nil, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "invalid browser control") {
		t.Fatalf("control error = %v", err)
	}
}

func TestBrowserMemoryHeadroomIgnoresShadowLeasePredictions(t *testing.T) {
	now := time.Now()
	// The M4 on 2026-11-17: 45% of 48 GB free while 13 leases, most of them
	// thin clients of remote gaterunner jobs, predicted 18.5 GB in flight.
	snapshot := Snapshot{Timestamp: now.Add(-5 * time.Second), PhysicalMemoryMB: 49_152, FreePercent: 45}
	headroom := browserMemoryHeadroom(snapshot, true, now)
	if !headroom.SampleFresh || headroom.InflightMB != 0 || headroom.HeadroomMB != headroom.AvailableMB-headroom.ReserveMB || headroom.HeadroomMB < 15_000 {
		t.Fatalf("headroom = %+v, want available minus reserve with no lease charge", headroom)
	}
	if decision := decideBrowserCapacity(headroom, nil, BrowserRuntimeChromeForTesting, browserChromeForTestingSessionMB); !decision.Admit {
		t.Fatalf("CFT session refused with %d MB headroom: %+v", headroom.HeadroomMB, decision)
	}
	if got := browserMemoryHeadroom(Snapshot{}, false, now); got.SampleFresh {
		t.Fatalf("no sample reported fresh: %+v", got)
	}
}
