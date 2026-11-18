package filelock

import (
	"time"

	"golang.org/x/sys/unix"
)

// darwinZombie is SZOMB from <sys/proc.h>: the process exited and waits for
// its parent to reap it.
const darwinZombie = 5

// platformProcessInfo reads pid's start time and zombie state from the
// kernel (sysctl kern.proc.pid). It reports false when the process is gone or
// the kernel does not answer.
func platformProcessInfo(pid int) (processInfo, bool) {
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(proc.Proc.P_pid) != pid {
		return processInfo{}, false
	}
	started := proc.Proc.P_starttime
	if started.Sec <= 0 {
		return processInfo{}, false
	}
	return processInfo{
		started: time.Unix(started.Sec, int64(started.Usec)*int64(time.Microsecond)),
		zombie:  proc.Proc.P_stat == darwinZombie,
	}, true
}
