//go:build linux

package sessionpressure

import "golang.org/x/sys/unix"

func processPeakRSSMB() float64 {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	// Linux reports ru_maxrss in KiB.
	return float64(usage.Maxrss) / 1024
}
