package helpermain

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"runtime/trace"
	"syscall"
	"time"
)

// CPUProfileSignal asks the resident for one bounded CPU profile of itself:
//
//	kill -USR2 <resident pid>
//
// The profile goes to <data dir>/profiles/cpu-<UTC time>.pprof. The first
// cpuTraceDuration of the window is also recorded as an execution trace,
// trace-<UTC time>.out, which names the goroutine behind every wakeup
// (go tool trace -pprof=sched). The resident
// guard has a 0.25% idle CPU budget, and its cost depends on live inputs (lease
// churn, disk writes, browser control) that a copy of the data dir cannot
// reproduce, so the installed process must be measurable in place. Nothing
// runs until the signal arrives; a signal during a profile is ignored.
const CPUProfileSignal = syscall.SIGUSR2

var cpuProfileDuration = 10 * time.Minute

// cpuTraceDuration bounds the execution trace. A trace records every
// scheduler event, so it stays short even though the profile runs longer.
var cpuTraceDuration = 30 * time.Second

func cpuProfilePath(dir string, now time.Time) string {
	return filepath.Join(dir, "profiles", "cpu-"+now.UTC().Format("20060102T150405Z")+".pprof")
}

func cpuTracePath(dir string, now time.Time) string {
	return filepath.Join(dir, "profiles", "trace-"+now.UTC().Format("20060102T150405Z")+".out")
}

// startTrace records an execution trace beside the profile. A trace failure
// never stops the CPU profile. stop is safe to call more than once.
func startTrace(path string) (stop func(), err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return func() {}, err
	}
	if err := trace.Start(file); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return func() {}, err
	}
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		trace.Stop()
		_ = file.Close()
	}, nil
}

// serveCPUProfiles takes one profile per signal until ctx ends. ready, when
// non-nil, is closed once the signal handler is installed.
func serveCPUProfiles(ctx context.Context, dir string, ready chan<- struct{}) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, CPUProfileSignal)
	defer signal.Stop(signals)
	if ready != nil {
		close(ready)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-signals:
			path, err := writeCPUProfile(ctx, dir, signals)
			if err != nil {
				fmt.Fprintln(os.Stderr, "session-pressure-helper: cpu profile:", err)
				continue
			}
			fmt.Fprintln(os.Stderr, "session-pressure-helper: cpu profile written:", path)
		}
	}
}

func writeCPUProfile(ctx context.Context, dir string, signals <-chan os.Signal) (string, error) {
	now := time.Now()
	path := cpuProfilePath(dir, now)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := pprof.StartCPUProfile(file); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	stopTrace, traceErr := startTrace(cpuTracePath(dir, now))
	if traceErr != nil {
		fmt.Fprintln(os.Stderr, "session-pressure-helper: execution trace:", traceErr)
	}
	defer stopTrace()
	traceTimer := time.NewTimer(min(cpuTraceDuration, cpuProfileDuration))
	defer traceTimer.Stop()
	timer := time.NewTimer(cpuProfileDuration)
	defer timer.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case <-traceTimer.C:
			stopTrace()
		case <-timer.C:
			break wait
		case <-signals:
			// One profile at a time; the running one keeps its window.
		}
	}
	stopTrace()
	pprof.StopCPUProfile()
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}
