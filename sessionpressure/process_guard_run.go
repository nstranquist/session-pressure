package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// RunBoundedProcess supervises only a newly launched private process group.
// Unlike interactive Stop, teardown of this owned group must still work if
// incident storage fails. A storage failure is returned, never hidden.
func RunBoundedProcess(ctx context.Context, dir string, timeout time.Duration, argv []string, stdin io.Reader, stdout, stderr io.Writer) (code int, resultErr error) {
	if timeout <= 0 || timeout > time.Hour || len(argv) == 0 {
		return 2, fmt.Errorf("run requires --timeout between 1ns and 1h and -- COMMAND [ARGS...]")
	}
	group, err := newBoundedProcessGroup(ctx)
	if err != nil {
		return 1, err
	}
	defer func() { resultErr = errors.Join(resultErr, group.close()) }()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: group.anchor.Process.Pid}
	cmd.WaitDelay = time.Second
	began := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	start, uid, identityErr := processGuardIdentity(cmd.Process.Pid)
	// No Wait has reaped this child yet, so its PID cannot have been reused.
	if identityErr != nil {
		killErr := group.close()
		waitErr := cmd.Wait()
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			// A fast command can finish before identity lookup. Preserve its
			// ordinary exit status, including a nonzero status.
			code, exitErr := boundedProcessExit(waitErr)
			return code, errors.Join(exitErr, killErr)
		}
		return 1, errors.Join(identityErr, killErr, waitErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var cause string
	select {
	case err := <-done:
		code, exitErr := boundedProcessExit(err)
		return code, errors.Join(exitErr, group.close())
	case <-ctx.Done():
		cause = "guard_cancelled"
	case <-timer.C:
		cause = "guard_deadline"
	}
	// Prefer completion if it raced with the timer.
	select {
	case err := <-done:
		code, exitErr := boundedProcessExit(err)
		return code, errors.Join(exitErr, group.close())
	default:
	}
	now := time.Now().UTC()
	incident := ProcessIncident{ID: incidentKey(cmd.Process.Pid, start), PID: cmd.Process.Pid, StartIdentity: start, UID: uid, Executable: filepath.Base(argv[0]), Reason: cause, State: "stop_requested", FirstSeen: began, LastSeen: now, AgeSeconds: int64(now.Sub(began).Seconds()), Actions: []ProcessIncidentAction{{At: now, Signal: "KILL", Result: "requested"}}}
	persistErr := updateProcessIncidents(context.Background(), dir, func(r *ProcessIncidentReport) error { r.Incidents = append(r.Incidents, incident); return nil })
	// The anchor pins the group independently of the command's lifetime. Kill
	// remaining descendants even when the command exits during persistence.
	signalErr := group.close()
	select {
	case <-done:
		if signalErr == nil {
			incident.State = "stopped"
			incident.Actions[0].Result = "exit_confirmed"
		} else {
			incident.State = "recovered"
			incident.Actions[0].Result = "no_signal"
			incident.Actions[0].Error = signalErr.Error()
		}
	case <-time.After(2 * time.Second):
		incident.Actions[0].Result = "signal_sent_unconfirmed"
	}
	resultErr = updateProcessIncidents(context.Background(), dir, func(r *ProcessIncidentReport) error {
		for i := range r.Incidents {
			if r.Incidents[i].ID == incident.ID {
				r.Incidents[i] = incident
				return nil
			}
		}
		r.Incidents = append(r.Incidents, incident)
		return nil
	})
	return 124, errors.Join(persistErr, resultErr, signalErr)
}

func boundedProcessExit(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if code < 0 {
			code = 1
		}
		return code, nil
	}
	return 1, err
}
