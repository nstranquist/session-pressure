package sessionpressure

import (
	"context"
	"sync"
	"time"
)

// Idle-lease parking.
//
// A lease charges its full weight for the life of the leased process group,
// but many groups spend long stretches doing nothing: an installer polling a
// busy service, a test blocked on a hung socket, a build waiting on another
// build's cache lock. The 2026-11-02 ledger held a 2-unit lease idle for 26
// minutes behind a full queue, and test leases of 97-151 minutes that ended
// child_failed. The wrapper already owns the child, so it measures the
// group's own CPU and releases the weight while the group is idle. Nothing is
// preempted: a parked group that wakes is re-charged at once, and the
// weighted ceiling plus every pressure gate keep governing new admissions.
const (
	defaultIdleParkSeconds = 120
	minIdleParkSeconds     = 30
	// Under 1% of one core is idle; at or above 5% the group is working. The
	// band between holds the current verdict so a light poller cannot flap.
	workIdleMaxCores  = 0.01
	workIdleWakeCores = 0.05
	// A group stalled on page faults burns little CPU but is not idle; under
	// swap thrash, parking it would admit more work into the thrash. Any
	// sustained page-in rate blocks parking, and a heavy one wakes the lease.
	workIdleMaxPageinsPerSecond  = 4.0
	workIdleWakePageinsPerSecond = 64.0
	// While parked, re-assert every few ticks so a field dropped by an n-1
	// writer is restored without waiting for a transition.
	workIdleReassertTicks = 4
)

// workIdleTick is a var only so tests can shorten it.
var workIdleTick = 15 * time.Second

// workIdleParkEligible excludes classes whose weight is not about the
// group's own CPU: benchmarks need a quiet host even between iterations,
// emulators and browsers do their real work in launchd-owned processes
// outside the group, and install/reclaim already weigh one unit.
func workIdleParkEligible(class WorkClass) bool {
	switch class {
	case WorkClassTest, WorkClassExpressTest, WorkClassBuild, WorkClassExpressBuild, WorkClassHeavy:
		return true
	default:
		return false
	}
}

// workGroupActivity is a cumulative process-group activity reading.
type workGroupActivity struct {
	CPUNS   uint64
	Pageins uint64
}

// workIdleDetector is the pure parking state machine. observe returns +1 to
// park, -1 to unpark, and 0 for no transition.
type workIdleDetector struct {
	idleAfter   time.Duration
	last        workGroupActivity
	lastAt      time.Time
	haveLast    bool
	idleSince   time.Time
	parked      bool
	parkedAt    time.Time
	parkedTotal time.Duration
	parkCount   int
}

func (detector *workIdleDetector) observe(now time.Time, sample workGroupActivity, ok bool) int {
	if !ok {
		// Missing evidence never keeps weight released.
		detector.haveLast = false
		detector.idleSince = time.Time{}
		return detector.setParked(now, false)
	}
	if !detector.haveLast {
		detector.last, detector.lastAt, detector.haveLast = sample, now, true
		if detector.idleSince.IsZero() {
			detector.idleSince = now
		}
		return 0
	}
	elapsed := now.Sub(detector.lastAt)
	if elapsed <= 0 {
		return 0
	}
	cpuDelta, pageinDelta := uint64(0), uint64(0)
	if sample.CPUNS > detector.last.CPUNS {
		cpuDelta = sample.CPUNS - detector.last.CPUNS
	}
	if sample.Pageins > detector.last.Pageins {
		pageinDelta = sample.Pageins - detector.last.Pageins
	}
	detector.last, detector.lastAt = sample, now
	cores := float64(cpuDelta) / float64(elapsed.Nanoseconds())
	pageins := float64(pageinDelta) / elapsed.Seconds()
	switch {
	case cores >= workIdleWakeCores || pageins >= workIdleWakePageinsPerSecond:
		detector.idleSince = now
		return detector.setParked(now, false)
	case cores > workIdleMaxCores || pageins > workIdleMaxPageinsPerSecond:
		// Light activity: not idle, but not enough to wake a parked lease.
		if !detector.parked {
			detector.idleSince = now
		}
		return 0
	default:
		if !detector.parked && now.Sub(detector.idleSince) >= detector.idleAfter {
			return detector.setParked(now, true)
		}
		return 0
	}
}

func (detector *workIdleDetector) setParked(now time.Time, parked bool) int {
	if parked == detector.parked {
		return 0
	}
	detector.parked = parked
	if parked {
		detector.parkedAt = now
		detector.parkCount++
		return 1
	}
	detector.parkedTotal += now.Sub(detector.parkedAt)
	detector.parkedAt = time.Time{}
	return -1
}

// revert undoes a transition whose durable write failed, so the detector
// never believes in weight the coordinator still charges (or vice versa).
func (detector *workIdleDetector) revert(transition int) {
	switch transition {
	case 1:
		detector.parked = false
		detector.parkedAt = time.Time{}
		detector.parkCount--
	case -1:
		detector.parked = true
		detector.parkedAt = detector.lastAt
	}
}

func (detector *workIdleDetector) totals(now time.Time) (time.Duration, int) {
	total := detector.parkedTotal
	if detector.parked {
		total += now.Sub(detector.parkedAt)
	}
	return total, detector.parkCount
}

// workIdleParking runs one detector against one leased process group.
type workIdleParking struct {
	mu       sync.Mutex
	detector workIdleDetector
	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}
}

type workIdleLease interface {
	SetParked(ctx context.Context, parked bool, at time.Time) error
}

// startWorkIdleParking returns nil when parking does not apply, so callers can
// treat a nil value as "never parked".
func startWorkIdleParking(lease workIdleLease, pgid int, class WorkClass, limits WorkLimits, sample func(int) (workGroupActivity, error)) *workIdleParking {
	limits = normalizeWorkLimits(limits)
	if lease == nil || pgid <= 0 || sample == nil || limits.IdleParkSeconds < 0 || !workIdleParkEligible(class) {
		return nil
	}
	parking := &workIdleParking{
		detector: workIdleDetector{idleAfter: time.Duration(limits.IdleParkSeconds) * time.Second},
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
	go parking.run(lease, pgid, sample)
	return parking
}

func (parking *workIdleParking) run(lease workIdleLease, pgid int, sample func(int) (workGroupActivity, error)) {
	defer close(parking.done)
	ticker := time.NewTicker(workIdleTick)
	defer ticker.Stop()
	observe := func(now time.Time) {
		activity, err := sample(pgid)
		parking.mu.Lock()
		transition := parking.detector.observe(now, activity, err == nil)
		parking.mu.Unlock()
		if transition == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		setErr := lease.SetParked(ctx, transition > 0, now)
		cancel()
		if setErr != nil {
			parking.mu.Lock()
			parking.detector.revert(transition)
			parking.mu.Unlock()
		}
	}
	observe(time.Now())
	ticks := 0
	for {
		select {
		case <-parking.stopCh:
			return
		case now := <-ticker.C:
			observe(now)
			ticks++
			parking.mu.Lock()
			parked := parking.detector.parked
			parking.mu.Unlock()
			if parked && ticks%workIdleReassertTicks == 0 {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = lease.SetParked(ctx, true, now)
				cancel()
			}
		}
	}
}

// stop ends sampling and returns the total parked time and park count. The
// lease itself is released by the caller, which also clears any parked state.
func (parking *workIdleParking) stop() (time.Duration, int) {
	if parking == nil {
		return 0, 0
	}
	parking.stopOnce.Do(func() { close(parking.stopCh) })
	<-parking.done
	parking.mu.Lock()
	defer parking.mu.Unlock()
	return parking.detector.totals(time.Now())
}
