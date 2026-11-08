//go:build darwin

package sessionpressure

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Avoid ps: formatting every process's full argv costs tens of milliseconds of
// child CPU per sample on a busy host. Kernel metadata narrows argv reads to
// same-user browser candidates; no other process's arguments are requested.
func automatedBrowserProcesses(ctx context.Context) ([]browserProcess, error) {
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	var roots []browserProcess
	var argsReader darwinProcessArgsReader
	for index := range rows {
		if index%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		row := rows[index]
		pid := int(row.Proc.P_pid)
		if pid <= 1 || int(row.Eproc.Ucred.Uid) != os.Getuid() {
			continue
		}
		comm := strings.ToLower(darwinCString(row.Proc.P_comm[:]))
		if !strings.Contains(comm, "chrome") && !strings.Contains(comm, "chromium") {
			continue
		}
		_, err := argsReader.args(pid)
		if err != nil {
			continue // Processes can exit while inventory is read.
		}
		automated, err := automatedBrowserKernelArgs(argsReader.buffer)
		if err != nil || !automated {
			continue
		}
		start := row.Proc.P_starttime
		roots = append(roots, browserProcess{PID: pid,
			StartedAt:     time.Unix(int64(start.Sec), int64(start.Usec)*1000),
			StartIdentity: fmt.Sprintf("darwin:%d:%d", start.Sec, start.Usec)})
	}
	return roots, nil
}
