package helpermain

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"syscall"
	"time"
)

// CPUProfileSignal asks the resident for one bounded CPU profile of itself:
//
//	kill -USR2 <resident pid>
//
// The profile goes to <data dir>/profiles/cpu-<UTC time>.pprof. The resident
// guard has a 0.25% idle CPU budget, and its cost depends on live inputs (lease
// churn, disk writes, browser control) that a copy of the data dir cannot
// reproduce, so the installed process must be measurable in place. Nothing
// runs until the signal arrives; a signal during a profile is ignored.
const CPUProfileSignal = syscall.SIGUSR2

var cpuProfileDuration = 10 * time.Minute

func cpuProfilePath(dir string, now time.Time) string {
	return filepath.Join(dir, "profiles", "cpu-"+now.UTC().Format("20060102T150405Z")+".pprof")
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
	path := cpuProfilePath(dir, time.Now())
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
wait:
	for {
		select {
		case <-ctx.Done():
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
	return path, nil
}
