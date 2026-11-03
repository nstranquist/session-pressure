//go:build linux

package sessionpressure

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func processPeakRSSMB() float64 {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	// Linux reports ru_maxrss in KiB.
	return float64(usage.Maxrss) / 1024
}

// processStatePeakRSSMB is the largest RSS of a reaped child and the
// descendants it waited for.
func processStatePeakRSSMB(state *os.ProcessState) (float64, bool) {
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil || usage.Maxrss <= 0 {
		return 0, false
	}
	return float64(usage.Maxrss) / 1024, true
}
