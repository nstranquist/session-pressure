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
