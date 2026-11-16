//go:build darwin

package sessionpressure

import "golang.org/x/sys/unix"

// listProcessRows lists pid and ppid with one sysctl. It reads no command
// lines and no per-process memory; processRSSKB reads memory for tree
// members only.
func listProcessRows() ([]processRow, error) {
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]processRow, 0, len(rows))
	for index := range rows {
		if pid := int(rows[index].Proc.P_pid); pid > 0 {
			out = append(out, processRow{pid: pid, ppid: int(rows[index].Eproc.Ppid)})
		}
	}
	return out, nil
}

// processRSSKB uses proc_pid_rusage, the resident inventory's source.
func processRSSKB(pid int) (int64, bool) {
	if loadDarwinLibproc() != nil {
		return 0, false
	}
	var usage darwinRusageInfoV2
	if darwinLibproc.pidRusageV2(int32(pid), darwinRusageInfoVersion2, &usage) != 0 {
		return 0, false
	}
	return int64(usage.ResidentSize / 1024), true
}
