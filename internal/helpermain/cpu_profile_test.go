package helpermain

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// One SIGUSR2 writes one bounded, non-empty profile under <dir>/profiles.
func TestCPUProfileSignalWritesOneBoundedProfile(t *testing.T) {
	previous, previousTrace := cpuProfileDuration, cpuTraceDuration
	cpuProfileDuration = 300 * time.Millisecond
	cpuTraceDuration = 100 * time.Millisecond
	t.Cleanup(func() { cpuProfileDuration, cpuTraceDuration = previous, previousTrace })
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveCPUProfiles(ctx, dir, ready)
	}()
	<-ready
	if err := syscall.Kill(os.Getpid(), CPUProfileSignal); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var profiles []string
	for time.Now().Before(deadline) {
		profiles, _ = filepath.Glob(filepath.Join(dir, "profiles", "cpu-*.pprof"))
		if len(profiles) == 1 {
			if info, err := os.Stat(profiles[0]); err == nil && info.Size() > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles = %v, want exactly one", profiles)
	}
	info, err := os.Stat(profiles[0])
	if err != nil || info.Size() == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("profile %s: size/mode wrong (%v, %v)", profiles[0], info, err)
	}
	// The trace follows the profile window; wait for it to be written.
	var traces []string
	for time.Now().Before(deadline) {
		traces, _ = filepath.Glob(filepath.Join(dir, "profiles", "trace-*.out"))
		if len(traces) == 1 {
			if info, err := os.Stat(traces[0]); err == nil && info.Size() > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(traces) != 1 {
		t.Fatalf("traces = %v, want exactly one after the profile", traces)
	}
	cancel()
	<-done
	if info, err := os.Stat(traces[0]); err != nil || info.Size() == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("trace %s: size/mode wrong (%v, %v)", traces[0], info, err)
	}
}

// Without a signal nothing is written.
func TestCPUProfileIdleWritesNothing(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveCPUProfiles(ctx, dir, ready)
	}()
	<-ready
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if _, err := os.Stat(filepath.Join(dir, "profiles")); !os.IsNotExist(err) {
		t.Fatalf("profiles dir exists without a signal: %v", err)
	}
}

// Stopping the server during a profile still writes it: a resident restart
// keeps the shorter window instead of leaving an empty file.
func TestCPUProfileStopFlushesProfileInProgress(t *testing.T) {
	previous, previousTrace := cpuProfileDuration, cpuTraceDuration
	cpuProfileDuration = time.Hour
	cpuTraceDuration = time.Hour
	t.Cleanup(func() { cpuProfileDuration, cpuTraceDuration = previous, previousTrace })
	dir := t.TempDir()
	stop := startCPUProfiles(context.Background(), dir, 5*time.Second)
	if err := syscall.Kill(os.Getpid(), CPUProfileSignal); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var profiles []string
	for len(profiles) == 0 && time.Now().Before(deadline) {
		profiles, _ = filepath.Glob(filepath.Join(dir, "profiles", "cpu-*.pprof"))
		time.Sleep(20 * time.Millisecond)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles = %v, want one started", profiles)
	}
	stop()
	files, _ := filepath.Glob(filepath.Join(dir, "profiles", "cpu-*.pprof"))
	if len(files) != 1 {
		t.Fatalf("profiles = %v, want one", files)
	}
	if info, err := os.Stat(files[0]); err != nil || info.Size() == 0 {
		t.Fatalf("%s is empty after stop (%v, %v)", files[0], info, err)
	}
	// An interrupted profile skips the trace instead of delaying shutdown.
	if traces, _ := filepath.Glob(filepath.Join(dir, "profiles", "trace-*.out")); len(traces) != 0 {
		t.Fatalf("interrupted profile wrote a trace: %v", traces)
	}
}
