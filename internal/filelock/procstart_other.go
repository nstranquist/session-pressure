//go:build !linux && !darwin

package filelock

import "time"

// platformProcessStartTime is not wired on this platform. A live PID then
// keeps its sidecar, as before WORK-1373.
func platformProcessStartTime(int) (time.Time, bool) { return time.Time{}, false }
