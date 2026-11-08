//go:build darwin

package sessionpressure

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func processGuardIdentity(pid int) (string, int, error) {
	// The single-row helper reports EIO when the PID disappears. The slice
	// helper preserves the kernel's empty result without masking lookup errors.
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", 0, err
	}
	if len(rows) == 0 {
		return "", 0, fmt.Errorf("PID %d: %w", pid, errProcessGuardExited)
	}
	if len(rows) != 1 || int(rows[0].Proc.P_pid) != pid {
		return "", 0, fmt.Errorf("unexpected process identity result for PID %d", pid)
	}
	row := &rows[0]
	if row.Proc.P_stat == 5 {
		return "", 0, fmt.Errorf("PID %d: %w", pid, errProcessGuardExited)
	}
	start := row.Proc.P_starttime
	if start.Sec == 0 && start.Usec == 0 {
		return "", 0, fmt.Errorf("missing process birth identity")
	}
	return fmt.Sprintf("darwin:%d:%d", start.Sec, start.Usec), int(row.Eproc.Ucred.Uid), nil
}
