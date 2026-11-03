package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Nested work runs.
//
// A leased command often runs another coordinated command: a Makefile target
// under `work run --class build` that itself calls `work run`, or a promotion
// script that builds and then installs. A nested acquisition used to queue
// behind its own parent: a 5-unit build inside a 5-unit build on a capacity of
// 8 waits for capacity that only its parent can free. The parent already holds
// capacity for its whole process tree, so a nested run executes inline under
// the parent lease instead of acquiring a second one.
//
// The inherited operation ID alone is not trusted. The parent lease must still
// be live in the coordinator, its leader must be alive, and this process must
// still be in the leased process group. A process that left the group (setsid,
// a daemon) or outlived the lease falls back to normal admission.

// WorkOperationEnv names the coordinated operation a leased command runs under.
// It is bookkeeping: excluded from the express reuse fingerprint.
const WorkOperationEnv = "SESSION_PRESSURE_WORK_OPERATION"

var (
	workProcessGroup = syscall.Getpgrp
	workProcessAlive = func(pid int) bool {
		err := syscall.Kill(pid, 0)
		return err == nil || errors.Is(err, syscall.EPERM)
	}
	// workNestedExec replaces this process with the nested command, so exit
	// status and signals behave as if the command ran directly.
	workNestedExec = syscall.Exec
)

const workNestedProbeTimeout = 2 * time.Second

func workEnvValue(environ []string, key string) string {
	for index := len(environ) - 1; index >= 0; index-- {
		if name, value, found := strings.Cut(environ[index], "="); found && name == key {
			return value
		}
	}
	return ""
}

// withWorkOperationEnv sets the operation a launched child runs under,
// replacing any value inherited from an outer, unrelated run.
func withWorkOperationEnv(environ []string, operationID string) []string {
	out := make([]string, 0, len(environ)+1)
	for _, value := range environ {
		if name, _, found := strings.Cut(value, "="); found && name == WorkOperationEnv {
			continue
		}
		out = append(out, value)
	}
	return append(out, WorkOperationEnv+"="+operationID)
}

// nestedWorkParent returns the live lease this process already runs under.
func nestedWorkParent(coordinator *WorkCoordinator, environ []string) (WorkLeaseStatus, bool) {
	operationID := strings.TrimSpace(workEnvValue(environ, WorkOperationEnv))
	if coordinator == nil || operationID == "" {
		return WorkLeaseStatus{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), workNestedProbeTimeout)
	defer cancel()
	status, err := coordinator.StatusSnapshot(ctx)
	if err != nil {
		return WorkLeaseStatus{}, false
	}
	group := workProcessGroup()
	for _, lease := range status.Leases {
		if lease.OperationID != operationID || lease.PID <= 0 {
			continue
		}
		if lease.PID == group && workProcessAlive(lease.PID) {
			return lease, true
		}
	}
	return WorkLeaseStatus{}, false
}

// runNestedWorkCommand runs the command inline under the parent lease. It
// returns only when the command could not be started.
func runNestedWorkCommand(parent WorkLeaseStatus, options WorkRunOptions, streams WorkRunStreams) (int, error) {
	resolved, err := exec.LookPath(options.Command[0])
	if err != nil {
		return 1, fmt.Errorf("resolve nested leased command: %w", err)
	}
	if options.Progress != WorkProgressQuiet && streams.Stderr != nil {
		fmt.Fprintf(streams.Stderr, "ndev session pressure: nested class=%s runs under parent operation=%s class=%s weight=%d\n",
			options.Class, parent.OperationID, parent.Class, parent.Weight)
	}
	if err := workNestedExec(resolved, options.Command, os.Environ()); err != nil {
		return 1, fmt.Errorf("exec nested leased command: %w", err)
	}
	return 0, nil
}
