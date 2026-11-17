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
	previous := cpuProfileDuration
	cpuProfileDuration = 300 * time.Millisecond
	t.Cleanup(func() { cpuProfileDuration = previous })
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
	cancel()
	<-done
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
