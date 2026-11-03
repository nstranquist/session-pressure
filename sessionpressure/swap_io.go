package sessionpressure

import (
	"math/bits"
	"time"
)

const bytesPerMiB = 1024 * 1024

// defaultSwapIOSource is the constructor default for both the memory sampler
// and the disk-write observer. Tests replace it so host swap never leaks into
// deterministic fixtures.
var defaultSwapIOSource = nativeSwapIOCounter

// defaultFreePercentSource follows the same rule: tests set it to nil so the
// fake memory_pressure runner stays authoritative.
var defaultFreePercentSource = nativeFreePercent

// swapIOCounter is the cumulative kernel swap traffic since boot. Swap-out is
// physical write traffic to the internal SSD that no process rusage counter
// owns, so it is both the live memory-thrash signal and the missing
// attribution row for the disk-write plane.
type swapIOCounter struct {
	SwapInBytes  uint64
	SwapOutBytes uint64
}

// swapIORate is a delta between two cumulative counters. Rates are reported
// in MiB/s to match every other memory field in the snapshot.
type swapIORate struct {
	InMBPerSecond  float64
	OutMBPerSecond float64
	Window         time.Duration
	OutBytes       uint64
}

// swapIORateBetween fails closed on counter regressions (reboot or sampler
// identity change) and on a non-positive window instead of emitting a spike.
func swapIORateBetween(previous, current swapIOCounter, window time.Duration) (swapIORate, bool) {
	if window <= 0 || current.SwapInBytes < previous.SwapInBytes || current.SwapOutBytes < previous.SwapOutBytes {
		return swapIORate{}, false
	}
	if previous == (swapIOCounter{}) {
		return swapIORate{}, false
	}
	seconds := window.Seconds()
	inDelta := current.SwapInBytes - previous.SwapInBytes
	outDelta := current.SwapOutBytes - previous.SwapOutBytes
	return swapIORate{
		InMBPerSecond:  float64(inDelta) / bytesPerMiB / seconds,
		OutMBPerSecond: float64(outDelta) / bytesPerMiB / seconds,
		Window:         window,
		OutBytes:       outDelta,
	}, true
}

func saturatingMulUint64(left, right uint64) uint64 {
	high, low := bits.Mul64(left, right)
	if high != 0 {
		return ^uint64(0)
	}
	return low
}

// Two consecutive resident samples enter and two release. At the economy
// cadence (30s while warning) that is roughly one minute either way: long
// enough to ignore a single page-in burst, short enough to stop a storm.
const (
	swapThrashEnterSamples   = 2
	swapThrashReleaseSamples = 2
)

const (
	// Five minutes of thrash after leased work is already small is long enough
	// for an agent burst to drain (the latch releases after two calm samples,
	// about one minute at the 30 s pressure cadence). It is short enough that a
	// browser- or editor-driven storm costs each queued build minutes, not the
	// whole storm.
	defaultSwapThrashExternalReleaseSeconds = 300
	minSwapThrashExternalReleaseSeconds     = 60
	// A quarter of RAM: one serialized Go build is 1-3 GB on a 16 GB host, so
	// leased work above this share is a real part of the memory demand.
	defaultSwapThrashAgentSharePercent = 25
)

// Closed vocabulary for why the latch holds. Only "external" lifts the derate.
const (
	// SwapThrashCauseAgents: leased work RSS is at or above the agent share.
	SwapThrashCauseAgents = "agents"
	// SwapThrashCauseUnattributed: leased work is small, but not yet for the
	// whole external-release window. The derate still holds.
	SwapThrashCauseUnattributed = "unattributed"
	// SwapThrashCauseExternal: leased work stayed small for the release window
	// and the storm went on, so holding agents cannot end it.
	SwapThrashCauseExternal = "external"
)

// SwapThrashTransitionEvent is the resident telemetry event for latch entry,
// release, and derate changes. A few per episode keep storm length and agent
// cost reconstructable from history at negligible bytes.
const SwapThrashTransitionEvent = "swap_thrash_transition"

// Closed transition states.
const (
	SwapThrashEntered        = "entered"
	SwapThrashDerateReleased = "derate_released"
	SwapThrashDerateResumed  = "derate_resumed"
	SwapThrashReleased       = "released"
)

type SwapThrashTransition struct {
	State string `json:"state"`
	// Resumed marks an entry that continues an episode across a resident
	// restart; Since is the original latch entry.
	Resumed            bool      `json:"resumed,omitempty"`
	Cause              string    `json:"cause,omitempty"`
	Since              time.Time `json:"since,omitzero"`
	EpisodeSeconds     float64   `json:"episode_seconds,omitempty"`
	ExternalSeconds    float64   `json:"external_seconds,omitempty"`
	SwapOutMBPerSecond float64   `json:"swap_out_mb_per_s"`
	SwapUsedMB         float64   `json:"swap_used_mb"`
	CoordinatedRSSMB   float64   `json:"coordinated_rss_mb"`
	CoordinatedLeases  int       `json:"coordinated_leases"`
	AgentShareMB       float64   `json:"agent_share_mb,omitempty"`
}

// swapThrashLatch is resident-only state. Values between Release and Thrash
// hold the current verdict, so a storm oscillating around one threshold cannot
// flap heavy-work admission.
type swapThrashLatch struct {
	latched   bool
	hot, calm int

	// Episode attribution. since is latch entry; quietSince starts the current
	// run of samples with leased RSS under the agent share. proven is sticky
	// for the episode: once the storm outlived small leased work, leased work
	// that later grows past the share re-engages the derate, and shrinking
	// again lifts it at once instead of paying the proof window again.
	since               time.Time
	quietSince          time.Time
	proven              bool
	external            bool
	externalSince       time.Time
	externalTotal       time.Duration
	cause               string
	lastExternalRelease time.Time
	// resumed is set by resume and cleared by the next advance, which then
	// records the continuation in history.
	resumed bool
}

// swapThrashResumeMaxAge bounds how stale a predecessor's verdict may be:
// six 30 s pressure samples. Older evidence says nothing about the storm now.
const swapThrashResumeMaxAge = 3 * time.Minute

// resume carries a latched episode across a resident restart from the last
// published snapshot. A restart mid-storm then neither drops the derate for
// two samples nor charges agents the external proof again.
func (latch *swapThrashLatch) resume(latest Snapshot, now time.Time) {
	if latch == nil || !latest.SwapIOAvailable || !latest.SwapThrashLatched {
		return
	}
	if age := now.Sub(latest.Timestamp); age < -5*time.Second || age > swapThrashResumeMaxAge {
		return
	}
	*latch = swapThrashLatch{latched: true, hot: swapThrashEnterSamples, since: latest.SwapThrashSince, cause: latest.SwapThrashCause, resumed: true}
	if latch.since.IsZero() {
		latch.since = latest.Timestamp
	}
	if latest.SwapThrashCause == SwapThrashCauseExternal {
		latch.proven, latch.external, latch.externalSince = true, true, latest.Timestamp
	}
}

func (latch *swapThrashLatch) observe(snapshot *Snapshot, thresholds Thresholds) {
	if latch == nil || snapshot == nil {
		return
	}
	if !snapshot.SwapIOAvailable || thresholds.SwapOutThrashMBPerSecond <= 0 {
		// A derate must never outlive its evidence. Unavailable counters fail
		// open for throughput; the memory floors remain independent.
		*latch = swapThrashLatch{}
		snapshot.SwapThrashLatched = false
		return
	}
	switch rate := snapshot.SwapOutMBPerSecond; {
	case rate >= thresholds.SwapOutThrashMBPerSecond:
		latch.hot, latch.calm = latch.hot+1, 0
	case rate <= thresholds.SwapOutReleaseMBPerSecond:
		latch.hot, latch.calm = 0, latch.calm+1
	default:
		latch.hot, latch.calm = 0, 0
	}
	if !latch.latched && latch.hot >= swapThrashEnterSamples {
		latch.latched = true
	}
	if latch.latched && latch.calm >= swapThrashReleaseSamples {
		latch.latched = false
	}
	snapshot.SwapThrashLatched = latch.latched
}

// advance runs the hysteretic latch, then attributes the episode. It returns a
// transition for latch entry, release, and each change in whether the derate
// defers work. Agent-share flaps that keep the derate active emit nothing.
func (latch *swapThrashLatch) advance(snapshot *Snapshot, thresholds Thresholds, limits WorkLimits) (SwapThrashTransition, bool) {
	if latch == nil || snapshot == nil {
		return SwapThrashTransition{}, false
	}
	limits = normalizeWorkLimits(limits)
	before := *latch
	latch.observe(snapshot, thresholds)
	now := snapshot.Timestamp
	switch {
	case !before.latched && latch.latched:
		latch.since, latch.quietSince = now, time.Time{}
		latch.proven, latch.external, latch.externalSince, latch.externalTotal = false, false, time.Time{}, 0
		if window := swapThrashReleaseWindow(limits); window > 0 && !before.lastExternalRelease.IsZero() && now.Sub(before.lastExternalRelease) < window {
			// The storm came back before a full window of calm. It was already
			// proven external; do not charge agents the proof again.
			latch.proven = true
		}
		latch.lastExternalRelease = before.lastExternalRelease
		latch.attribute(snapshot, limits)
		return latch.transition(SwapThrashEntered, snapshot, limits), true
	case before.latched && !latch.latched:
		externalTotal := before.externalTotal
		if before.external {
			externalTotal += now.Sub(before.externalSince)
		}
		cause := before.cause
		if before.proven {
			cause = SwapThrashCauseExternal
		}
		transition := SwapThrashTransition{
			State: SwapThrashReleased, Cause: cause, Since: before.since,
			EpisodeSeconds: max(0, now.Sub(before.since).Seconds()), ExternalSeconds: max(0, externalTotal.Seconds()),
		}
		latch.fillEvidence(&transition, snapshot, limits)
		hot, calm := latch.hot, latch.calm
		*latch = swapThrashLatch{hot: hot, calm: calm}
		if before.proven {
			latch.lastExternalRelease = now
		}
		snapshot.SwapThrashCause, snapshot.SwapThrashSince = "", time.Time{}
		return transition, true
	case latch.latched:
		latch.attribute(snapshot, limits)
		if before.resumed {
			latch.resumed = false
			transition := latch.transition(SwapThrashEntered, snapshot, limits)
			transition.Resumed = true
			return transition, true
		}
		if latch.external != before.external {
			state := SwapThrashDerateResumed
			if latch.external {
				state = SwapThrashDerateReleased
			}
			return latch.transition(state, snapshot, limits), true
		}
	}
	return SwapThrashTransition{}, false
}

// attribute sets the episode cause on a latched snapshot. Missing inventory
// or memory size counts as small leased work: an observability gap must not
// hold agents for a whole storm.
func (latch *swapThrashLatch) attribute(snapshot *Snapshot, limits WorkLimits) {
	now := snapshot.Timestamp
	shareMB := swapThrashAgentShareMB(snapshot, limits)
	if shareMB > 0 && swapThrashCoordinatedRSSMB(snapshot) >= shareMB {
		latch.quietSince = time.Time{}
		latch.setExternal(false, now)
		latch.cause = SwapThrashCauseAgents
	} else {
		if latch.quietSince.IsZero() {
			latch.quietSince = now
		}
		window := swapThrashReleaseWindow(limits)
		if window > 0 && (latch.proven || now.Sub(latch.quietSince) >= window) {
			latch.proven = true
			latch.setExternal(true, now)
			latch.cause = SwapThrashCauseExternal
		} else {
			latch.setExternal(false, now)
			latch.cause = SwapThrashCauseUnattributed
		}
	}
	snapshot.SwapThrashCause, snapshot.SwapThrashSince = latch.cause, latch.since
}

func (latch *swapThrashLatch) setExternal(external bool, now time.Time) {
	switch {
	case external && !latch.external:
		latch.external, latch.externalSince = true, now
	case !external && latch.external:
		latch.externalTotal += now.Sub(latch.externalSince)
		latch.external, latch.externalSince = false, time.Time{}
	}
}

func (latch *swapThrashLatch) transition(state string, snapshot *Snapshot, limits WorkLimits) SwapThrashTransition {
	transition := SwapThrashTransition{State: state, Cause: latch.cause, Since: latch.since}
	latch.fillEvidence(&transition, snapshot, limits)
	return transition
}

func (latch *swapThrashLatch) fillEvidence(transition *SwapThrashTransition, snapshot *Snapshot, limits WorkLimits) {
	transition.SwapOutMBPerSecond = snapshot.SwapOutMBPerSecond
	transition.SwapUsedMB = snapshot.SwapUsedMB
	transition.CoordinatedRSSMB = swapThrashCoordinatedRSSMB(snapshot)
	if snapshot.CoordinatedWork.Available {
		transition.CoordinatedLeases = snapshot.CoordinatedWork.LeaseCount
	}
	transition.AgentShareMB = swapThrashAgentShareMB(snapshot, limits)
}

// swapThrashReleaseWindow is zero when the policy is strict (negative).
func swapThrashReleaseWindow(limits WorkLimits) time.Duration {
	if limits.SwapThrashExternalReleaseSeconds <= 0 {
		return 0
	}
	return time.Duration(limits.SwapThrashExternalReleaseSeconds) * time.Second
}

func swapThrashAgentShareMB(snapshot *Snapshot, limits WorkLimits) float64 {
	if snapshot.PhysicalMemoryMB <= 0 || limits.SwapThrashAgentSharePercent <= 0 {
		return 0
	}
	return snapshot.PhysicalMemoryMB * float64(limits.SwapThrashAgentSharePercent) / 100
}

func swapThrashCoordinatedRSSMB(snapshot *Snapshot) float64 {
	if !snapshot.CoordinatedWork.Available {
		return 0
	}
	return max(0, snapshot.CoordinatedWork.RSSSumMB)
}
