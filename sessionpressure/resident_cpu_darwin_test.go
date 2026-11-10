//go:build darwin

package sessionpressure

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt in to an actual monitor profile with -test.cpuprofile. Use isolated
// telemetry and no browser opt-in or automatic relief, so this diagnostic
// cannot signal an existing process or replace resident evidence.
func TestResidentMonitorCPUProfile(t *testing.T) {
	if os.Getenv("PRESSURE_PROFILE_MONITOR") != "1" {
		t.Skip("set PRESSURE_PROFILE_MONITOR=1 for a bounded native monitor profile")
	}
	dir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	policy, _, err := LoadPolicy(PolicyPath(dir), 16*1024)
	if err != nil {
		t.Fatal(err)
	}
	policy.AutoShedCritical = false
	store := NewTelemetryStore(t.TempDir())
	sampler := NewResidentSampler().WithWorkCoordinator(NewWorkCoordinator(dir, policy.WorkLimits))
	monitor := NewMonitor(sampler, store, policy)
	monitor.DiskObserver = NewDiskWriteObserver(store.Dir, policy.DiskWrite)
	cpuStarted := processCPUTime()
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := monitor.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("monitor CPU duty %.4f%%", 100*float64(processCPUTime()-cpuStarted)/float64(time.Since(started)))
}

// BenchmarkResidentCPUComponents permits profiling the native resident work
// independently of sleep intervals and subprocess wall time.
func BenchmarkResidentCPUComponents(b *testing.B) {
	ctx := context.Background()
	b.Run("agent_trees", func(b *testing.B) {
		processes := make([]Process, 2048)
		for i := range processes {
			processes[i] = Process{PID: i + 1, PPID: 1, Executable: "daemon", CPUAvailable: true}
		}
		processes[10].Agent = "codex"
		for b.Loop() {
			_ = buildAgentTrees(processes)
		}
	})
	b.Run("disk_process_counters", func(b *testing.B) {
		for b.Loop() {
			if _, err := nativeDiskProcessCounters(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("disk_observer", func(b *testing.B) {
		observer := NewDiskWriteObserver(b.TempDir(), DefaultPolicy(16*1024).DiskWrite)
		at := time.Now()
		for b.Loop() {
			at = at.Add(15 * time.Second)
			observer.Sample(ctx, at)
		}
	})
	b.Run("browser_inventory", func(b *testing.B) {
		for b.Loop() {
			if _, err := automatedBrowserProcesses(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
}
