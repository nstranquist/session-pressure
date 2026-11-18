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
// The profile goes to <data dir>/profiles/cpu-<UTC time>.pprof. Right after
// the profile window, cpuTraceDuration is recorded as an execution trace,
// trace-<UTC time>.out, which names the goroutine behind every wakeup
// (go tool trace -d=parsed). Note: the profiler itself wakes every 100 ms
// (runtime/pprof profileWriter), so on this mostly idle process the
// scheduler share of a profile (runtime.schedule, wakep, kevent) is largely
// the measurement, not the resident. The resident
// guard has a 0.25% idle CPU budget, and its cost depends on live inputs (lease
// churn, disk writes, browser control) that a copy of the data dir cannot
// reproduce, so the installed process must be measurable in place. Nothing
// runs until the signal arrives; a signal during a profile is ignored.
const CPUProfileSignal = syscall.SIGUSR2

var cpuProfileDuration = 10 * time.Minute

// cpuTraceDuration bounds the execution trace that follows the profile. A
// trace records every scheduler event, so it stays short.
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

// startCPUProfiles serves profiles until the returned stop is called. stop
// ends the server and waits up to flushWait for a profile in progress to be
// written, so a resident restart mid-window keeps a shorter profile instead of
// an empty file.
func startCPUProfiles(ctx context.Context, dir string, flushWait time.Duration) (stop func()) {
	profileCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	ready := make(chan struct{})
	go func() {
		defer close(done)
		serveCPUProfiles(profileCtx, dir, ready)
	}()
	<-ready
	return func() {
		cancel()
		timer := time.NewTimer(flushWait)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
	}
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
	timer := time.NewTimer(cpuProfileDuration)
	defer timer.Stop()
	interrupted := false
wait:
	for {
		select {
		case <-ctx.Done():
			interrupted = true
			break wait
		case <-timer.C:
			break wait
		case <-signals:
			// One profile at a time; the running one keeps its window.
		}
	}
	pprof.StopCPUProfile()
	if err := file.Close(); err != nil {
		return "", err
	}
	if !interrupted {
		writeTrace(ctx, cpuTracePath(dir, now), signals)
	}
	return path, nil
}

// writeTrace records the execution trace after the profile window, so the
// trace's own cost never lands in the CPU profile and profiles stay
// comparable with earlier ones.
func writeTrace(ctx context.Context, path string, signals <-chan os.Signal) {
	stop, err := startTrace(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "session-pressure-helper: execution trace:", err)
		return
	}
	defer stop()
	timer := time.NewTimer(cpuTraceDuration)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-signals:
			// The trace finishes the same request; a new signal is ignored.
		}
	}
}
