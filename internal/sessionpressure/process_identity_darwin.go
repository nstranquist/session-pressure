//go:build darwin

package sessionpressure

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// processStartIdentity reads only one kernel process row and is called at work
// lease boundaries, never from the resident sampling loop.
func processStartIdentity(pid int) (string, error) {
	row, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if row == nil || int(row.Proc.P_pid) != pid {
		return "", fmt.Errorf("PID %d is not live", pid)
	}
	started := row.Proc.P_starttime
	if started.Sec == 0 && started.Usec == 0 {
		return "", fmt.Errorf("PID %d has no kernel start timestamp", pid)
	}
	return fmt.Sprintf("darwin:%d:%d", started.Sec, started.Usec), nil
}
