//go:build darwin

package sessionpressure

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Avoid ps: formatting every process's full argv costs tens of milliseconds of
// child CPU per sample on a busy host. Kernel metadata narrows argv reads to
// same-user browser candidates; no other process's arguments are requested.
//
// The resident scans on every sample. Argument vectors are fixed at exec, so
// the result for one process is kept until it exits: the key is the pid, the
// kernel start time and the command name, and exec changes the name. Without
// this memo every personal Chrome helper's argv was read again each sample
// (WORK-1141).
type browserArgsKey struct {
	pid   int
	start unix.Timeval
	comm  string
}

type browserArgsResult struct {
	automated   bool
	userDataDir string
}

var browserArgsMemo = struct {
	sync.Mutex
	results map[browserArgsKey]browserArgsResult
}{results: make(map[browserArgsKey]browserArgsResult)}

func automatedBrowserProcesses(ctx context.Context) ([]browserProcess, error) {
	// Only this user's browsers are controlled. KERN_PROC_UID matches the
	// same effective uid the loop checks, and the kernel copies out only
	// those rows instead of the whole process table.
	uid := os.Getuid()
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.uid", uid)
	if err != nil {
		return nil, err
	}
	browserArgsMemo.Lock()
	defer browserArgsMemo.Unlock()
	previous := browserArgsMemo.results
	current := make(map[browserArgsKey]browserArgsResult, len(previous))
	var roots []browserProcess
	var argsReader darwinProcessArgsReader
	for index := range rows {
		if index%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		row := &rows[index]
		pid := int(row.Proc.P_pid)
		if pid <= 1 || int(row.Eproc.Ucred.Uid) != uid {
			continue
		}
		comm := strings.ToLower(darwinCString(row.Proc.P_comm[:]))
		if !strings.Contains(comm, "chrome") && !strings.Contains(comm, "chromium") {
			continue
		}
		start := row.Proc.P_starttime
		key := browserArgsKey{pid: pid, start: start, comm: comm}
		result, known := previous[key]
		if !known {
			if _, err := argsReader.args(pid); err != nil {
				continue // Processes can exit while inventory is read.
			}
			automated, userDataDir, err := browserKernelArgs(argsReader.buffer)
			if err != nil {
				continue // Not cached: an unreadable argv is read again next time.
			}
			result = browserArgsResult{automated: automated, userDataDir: userDataDir}
		}
		current[key] = result
		if !result.automated {
			continue
		}
		roots = append(roots, browserProcess{PID: pid,
			StartedAt:     time.Unix(int64(start.Sec), int64(start.Usec)*1000),
			StartIdentity: fmt.Sprintf("darwin:%d:%d", start.Sec, start.Usec),
			UserDataDir:   result.userDataDir})
	}
	browserArgsMemo.results = current
	return roots, nil
}
