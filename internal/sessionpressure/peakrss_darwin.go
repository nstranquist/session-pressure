//go:build darwin

package sessionpressure

import "golang.org/x/sys/unix"

func processPeakRSSMB() float64 {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	// Darwin reports ru_maxrss in bytes.
	return float64(usage.Maxrss) / (1024 * 1024)
}
