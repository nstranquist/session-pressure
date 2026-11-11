//go:build darwin

package sessionpressure

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func processPeakRSSMB() float64 {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	// Darwin reports ru_maxrss in bytes.
	return float64(usage.Maxrss) / (1024 * 1024)
}

// processStatePeakRSSMB is the largest RSS of a reaped child and the
// descendants it waited for.
func processStatePeakRSSMB(state *os.ProcessState) (float64, bool) {
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil || usage.Maxrss <= 0 {
		return 0, false
	}
	return float64(usage.Maxrss) / (1024 * 1024), true
}

// platformCurrentRSSSource reads only this process; it does not enumerate the
// host or reuse the inventory cache. ResidentSize uses the same RSS semantics
// as the inventory, so the existing memory budget remains comparable.
func platformCurrentRSSSource() func() (float64, error) {
	return func() (float64, error) {
		if err := loadDarwinLibproc(); err != nil {
			return 0, err
		}
		var usage darwinRusageInfoV0
		if darwinLibproc.pidRusage(int32(os.Getpid()), darwinRusageInfoVersion0, &usage) != 0 {
			return 0, fmt.Errorf("proc_pid_rusage failed")
		}
		return float64(usage.ResidentSize) / (1024 * 1024), nil
	}
}
