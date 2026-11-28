//go:build !linux && !darwin

package filelock

// platformProcessInfo is not wired on this platform. A live PID then keeps
// its sidecar, as before the PID-reuse check.
func platformProcessInfo(int) (processInfo, bool) { return processInfo{}, false }
