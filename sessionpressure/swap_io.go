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

// swapThrashLatch is resident-only state. Values between Release and Thrash
// hold the current verdict, so a storm oscillating around one threshold cannot
// flap heavy-work admission.
type swapThrashLatch struct {
	latched   bool
	hot, calm int
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
