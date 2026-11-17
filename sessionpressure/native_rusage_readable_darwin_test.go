//go:build darwin

package sessionpressure

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// The scans skip proc_pid_rusage for rows the kernel would refuse. Prove on
// the live process table that a skipped row never had a readable counter, so
// the skip changes cost and nothing else.
func TestDarwinRusageReadableSkipsOnlyRefusedRows(t *testing.T) {
	if err := loadDarwinLibproc(); err != nil {
		t.Skip(err)
	}
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		t.Fatal(err)
	}
	euid := uint32(os.Geteuid())
	readable, skipped := 0, 0
	for index := range rows {
		pid := rows[index].Proc.P_pid
		if pid <= 0 {
			continue
		}
		if darwinRusageReadable(&rows[index], euid) {
			readable++
			continue
		}
		skipped++
		var usage darwinRusageInfoV2
		if darwinLibproc.pidRusageV2(pid, darwinRusageInfoVersion2, &usage) == 0 {
			t.Fatalf("pid %d uid %d is skipped but proc_pid_rusage succeeded", pid, rows[index].Eproc.Ucred.Uid)
		}
	}
	if readable == 0 {
		t.Fatal("no readable rows: the own-uid check is wrong")
	}
	if euid == 0 && skipped != 0 {
		t.Fatalf("root skipped %d rows", skipped)
	}
}
