//go:build darwin

package sessionpressure

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func processGuardIdentity(pid int) (string, int, error) {
	row, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", 0, err
	}
	if row == nil || int(row.Proc.P_pid) != pid || row.Proc.P_stat == 5 {
		return "", 0, fmt.Errorf("PID %d: %w", pid, errProcessGuardExited)
	}
	start := row.Proc.P_starttime
	if start.Sec == 0 && start.Usec == 0 {
		return "", 0, fmt.Errorf("missing process birth identity")
	}
	return fmt.Sprintf("darwin:%d:%d", start.Sec, start.Usec), int(row.Eproc.Ucred.Uid), nil
}
