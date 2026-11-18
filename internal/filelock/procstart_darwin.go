package filelock

import (
	"time"

	"golang.org/x/sys/unix"
)

// platformProcessStartTime reads the kernel's start time for pid
// (sysctl kern.proc.pid). It reports false when the process is gone or the
// kernel does not answer.
func platformProcessStartTime(pid int) (time.Time, bool) {
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(proc.Proc.P_pid) != pid {
		return time.Time{}, false
	}
	started := proc.Proc.P_starttime
	if started.Sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(started.Sec, int64(started.Usec)*int64(time.Microsecond)), true
}
