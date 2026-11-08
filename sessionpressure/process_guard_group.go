package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// A sleeping shell pins the private process-group ID until cleanup. Reaping the
// user's command cannot release or recycle this ID. Closing the control pipe
// (including supervisor death) makes the anchor kill its own group. The fixed
// shell program accepts no command text, arguments, or input from the user.
type boundedProcessGroup struct {
	anchor  *exec.Cmd
	control io.WriteCloser
	closed  bool
}

func newBoundedProcessGroup(ctx context.Context) (*boundedProcessGroup, error) {
	anchor := exec.Command("/bin/sh", "-c", "printf 'ready\\n'; IFS= read -r control; kill -s KILL -- -$$")
	anchor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	control, err := anchor.StdinPipe()
	if err != nil {
		return nil, err
	}
	ready, err := anchor.StdoutPipe()
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	if err := anchor.Start(); err != nil {
		_ = control.Close()
		_ = ready.Close()
		return nil, err
	}
	group := &boundedProcessGroup{anchor: anchor, control: control}
	done := make(chan error, 1)
	go func() {
		var word [6]byte
		_, err := io.ReadFull(ready, word[:])
		if err == nil && string(word[:]) != "ready\n" {
			err = fmt.Errorf("invalid process-group readiness")
		}
		done <- err
	}()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = fmt.Errorf("process-group readiness timeout")
	}
	_ = ready.Close()
	if err != nil {
		return nil, errors.Join(err, group.close())
	}
	return group, nil
}

func (group *boundedProcessGroup) close() error {
	if group.closed {
		return nil
	}
	group.closed = true
	// The unreaped anchor owns and pins this group even if it has exited.
	killErr := syscall.Kill(-group.anchor.Process.Pid, syscall.SIGKILL)
	if errors.Is(killErr, syscall.ESRCH) {
		killErr = nil
	}
	closeErr := group.control.Close()
	waitErr := group.anchor.Wait()
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
			waitErr = nil
		}
	}
	return errors.Join(killErr, closeErr, waitErr)
}
