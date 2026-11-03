package sessionpressure

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkIdleDetectorParksAfterSustainedIdleAndWakesOnWork(t *testing.T) {
	start := time.Date(2026, 11, 2, 23, 0, 0, 0, time.UTC)
	detector := workIdleDetector{idleAfter: 2 * time.Minute}
	cpu := uint64(0)
	at := start
	step := func(cores float64) int {
		at = at.Add(15 * time.Second)
		cpu += uint64(cores * float64(15*time.Second))
		return detector.observe(at, workGroupActivity{CPUNS: cpu}, true)
	}
	if got := detector.observe(at, workGroupActivity{CPUNS: cpu}, true); got != 0 {
		t.Fatalf("baseline transition=%d", got)
	}
	// 105s of idle is not enough; the 120s mark parks.
	for i := 0; i < 7; i++ {
		if got := step(0.002); got != 0 {
			t.Fatalf("idle tick %d parked early", i)
		}
	}
	if got := step(0.002); got != 1 {
		t.Fatalf("120s idle did not park: %d", got)
	}
	// A light poller (between idle and wake) holds the parked verdict.
	if got := step(0.03); got != 0 || !detector.parked {
		t.Fatalf("light activity flapped a parked lease: %d", got)
	}
	// Real work re-charges at once.
	if got := step(0.8); got != -1 || detector.parked {
		t.Fatalf("work did not unpark: %d", got)
	}
	parked, count := detector.totals(at)
	if count != 1 || parked != 30*time.Second {
		t.Fatalf("totals parked=%s count=%d", parked, count)
	}
	// Light activity while charged resets the idle clock.
	for i := 0; i < 7; i++ {
		step(0.002)
	}
	if got := step(0.03); got != 0 {
		t.Fatalf("light activity parked: %d", got)
	}
	if got := step(0.002); got != 0 || detector.parked {
		t.Fatal("idle clock was not reset by light activity")
	}
	// Lost evidence unparks and restarts the baseline.
	detector.parked, detector.parkedAt = true, at
	if got := detector.observe(at.Add(time.Second), workGroupActivity{}, false); got != -1 || detector.haveLast {
		t.Fatalf("missing evidence kept weight released: %d", got)
	}
	// A shrinking group total (exited, unreaped child) is not activity.
	fresh := workIdleDetector{idleAfter: 30 * time.Second}
	fresh.observe(start, workGroupActivity{CPUNS: 10_000_000_000}, true)
	if got := fresh.observe(start.Add(15*time.Second), workGroupActivity{CPUNS: 1_000}, true); got != 0 {
		t.Fatalf("regression transition=%d", got)
	}
	if got := fresh.observe(start.Add(30*time.Second), workGroupActivity{CPUNS: 1_000}, true); got != 1 {
		t.Fatalf("idle after regression did not park: %d", got)
	}
}

// A group stalled on page faults under swap thrash burns almost no CPU. It
// must not park (that would admit more work into the thrash), and a parked
// group that starts paging heavily is re-charged.
func TestWorkIdleDetectorTreatsPageinsAsActivity(t *testing.T) {
	start := time.Date(2026, 11, 2, 23, 0, 0, 0, time.UTC)
	detector := workIdleDetector{idleAfter: 30 * time.Second}
	at, sample := start, workGroupActivity{}
	step := func(pageinsPerSecond float64) int {
		at = at.Add(15 * time.Second)
		sample.Pageins += uint64(pageinsPerSecond * 15)
		return detector.observe(at, sample, true)
	}
	detector.observe(at, sample, true)
	for i := 0; i < 8; i++ {
		if got := step(20); got != 0 || detector.parked {
			t.Fatalf("page-faulting group parked at tick %d", i)
		}
	}
	// The idle clock starts at the last paging tick: 15s is not enough, 30s parks.
	if got := step(0); got != 0 {
		t.Fatalf("quiet group parked after 15s: %d", got)
	}
	if got := step(0); got != 1 {
		t.Fatalf("quiet group did not park after 30s: %d", got)
	}
	if got := step(20); got != 0 || !detector.parked {
		t.Fatal("light paging flapped a parked lease")
	}
	if got := step(500); got != -1 {
		t.Fatalf("thrashing group was not re-charged: %d", got)
	}
}

func TestWorkIdleDetectorRevertKeepsBeliefEqualToCoordinator(t *testing.T) {
	now := time.Date(2026, 11, 2, 23, 0, 0, 0, time.UTC)
	detector := workIdleDetector{idleAfter: time.Second}
	detector.observe(now, workGroupActivity{}, true)
	if got := detector.observe(now.Add(2*time.Second), workGroupActivity{}, true); got != 1 {
		t.Fatalf("park=%d", got)
	}
	detector.revert(1)
	if _, count := detector.totals(now); detector.parked || count != 0 {
		t.Fatalf("revert left parked=%v count=%d", detector.parked, count)
	}
}

func TestParkedLeaseReleasesWeightAndRechargesWithoutPreemption(t *testing.T) {
	limits := defaultWorkLimits(10) // capacity 8, build 5, test 3
	coordinator := NewWorkCoordinator(t.TempDir(), limits)
	ctx := context.Background()
	idle, status, err := coordinator.Acquire(ctx, WorkClassBuild)
	if err != nil || status.Used != 5 {
		t.Fatalf("first build used=%d err=%v", status.Used, err)
	}
	defer func() { _ = idle.Release(ctx) }()
	if _, _, err := coordinator.Acquire(ctx, WorkClassBuild); !errors.Is(err, ErrWorkCapacity) {
		t.Fatalf("second build admitted beside a charged build: %v", err)
	}
	parkedAt := time.Date(2026, 11, 2, 23, 0, 0, 0, time.UTC)
	if err := idle.SetParked(ctx, true, parkedAt); err != nil {
		t.Fatal(err)
	}
	// Idempotent: a re-assert keeps the original stamp.
	if err := idle.SetParked(ctx, true, parkedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, err = coordinator.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Used != 0 || status.ParkedWeight != 5 || len(status.Leases) != 1 || !status.Leases[0].Parked ||
		status.Leases[0].ParkedAt == nil || !status.Leases[0].ParkedAt.Equal(parkedAt) {
		t.Fatalf("parked status=%+v", status)
	}
	active, status, err := coordinator.Acquire(ctx, WorkClassBuild)
	if err != nil || status.Used != 5 {
		t.Fatalf("parked weight was not released: used=%d err=%v", status.Used, err)
	}
	defer func() { _ = active.Release(ctx) }()
	// The idle group wakes: it is re-charged even past capacity, and nothing
	// running is preempted; only new admissions wait.
	if err := idle.SetParked(ctx, false, parkedAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, err = coordinator.Status(ctx)
	if err != nil || status.Used != 10 || status.ParkedWeight != 0 || status.Available != 0 {
		t.Fatalf("recharged status used=%d parked=%d available=%d err=%v", status.Used, status.ParkedWeight, status.Available, err)
	}
	if _, _, err := coordinator.Acquire(ctx, WorkClassTest); !errors.Is(err, ErrWorkCapacity) {
		t.Fatalf("admission past an overcommitted ceiling: %v", err)
	}
	if err := idle.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := idle.SetParked(ctx, true, parkedAt); !errors.Is(err, ErrWorkLeaseMissing) {
		t.Fatalf("parking a released lease err=%v", err)
	}
}

type fakeIdleLease struct {
	mu     sync.Mutex
	calls  []bool
	failOn int
}

func (lease *fakeIdleLease) SetParked(_ context.Context, parked bool, _ time.Time) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.calls = append(lease.calls, parked)
	if lease.failOn > 0 && len(lease.calls) == lease.failOn {
		return errors.New("state lock timeout")
	}
	return nil
}

func (lease *fakeIdleLease) snapshot() []bool {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return append([]bool(nil), lease.calls...)
}

func TestWorkIdleParkingParksWakesAndReportsTotals(t *testing.T) {
	previous := workIdleTick
	workIdleTick = 20 * time.Millisecond
	defer func() { workIdleTick = previous }()
	var mu sync.Mutex
	cpu, busy := uint64(0), false
	sample := func(int) (workGroupActivity, error) {
		mu.Lock()
		defer mu.Unlock()
		if busy {
			cpu += uint64(20 * time.Millisecond) // one full core per tick
		}
		return workGroupActivity{CPUNS: cpu}, nil
	}
	lease := &fakeIdleLease{failOn: 1} // the first park write fails and is retried
	limits := defaultWorkLimits(10)
	limits.IdleParkSeconds = 1
	parking := startWorkIdleParking(lease, 4242, WorkClassTest, limits, sample)
	if parking == nil {
		t.Fatal("eligible class did not start parking")
	}
	waitFor := func(what string, ok func([]bool) bool) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if ok(lease.snapshot()) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s: calls=%v", what, lease.snapshot())
	}
	waitFor("park retry", func(calls []bool) bool { return len(calls) >= 2 && calls[1] })
	mu.Lock()
	busy = true
	mu.Unlock()
	waitFor("wake", func(calls []bool) bool { return len(calls) >= 3 && !calls[len(calls)-1] })
	parked, count := parking.stop()
	if count != 1 || parked <= 0 {
		t.Fatalf("totals parked=%s count=%d", parked, count)
	}
	if again, _ := parking.stop(); again != parked && again < parked {
		t.Fatalf("stop is not idempotent: %s vs %s", again, parked)
	}
	for _, class := range []WorkClass{WorkClassBenchmark, WorkClassBenchmarkExclusive, WorkClassEmulator, WorkClassBrowser, WorkClassInstall, WorkClassReclaim} {
		if startWorkIdleParking(lease, 4242, class, limits, sample) != nil {
			t.Fatalf("%s must never park", class)
		}
	}
	limits.IdleParkSeconds = -1
	if startWorkIdleParking(lease, 4242, WorkClassTest, limits, sample) != nil {
		t.Fatal("disabled parking started")
	}
	var nilParking *workIdleParking
	if parked, count := nilParking.stop(); parked != 0 || count != 0 {
		t.Fatal("nil parking reported totals")
	}
}

func TestIdleParkPolicyValidationAndLegacyFill(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	if policy.WorkLimits.IdleParkSeconds != defaultIdleParkSeconds {
		t.Fatalf("default idle park=%d", policy.WorkLimits.IdleParkSeconds)
	}
	policy.WorkLimits.IdleParkSeconds = 10
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "idle_park_seconds") {
		t.Fatalf("10s idle park validated: %v", err)
	}
	policy.WorkLimits.IdleParkSeconds = -1
	if err := policy.Validate(); err != nil {
		t.Fatalf("disabled idle park rejected: %v", err)
	}
	if got := normalizeWorkLimits(WorkLimits{Capacity: 8, BuildWeight: 5}).IdleParkSeconds; got != defaultIdleParkSeconds {
		t.Fatalf("legacy limits idle park=%d", got)
	}
}
