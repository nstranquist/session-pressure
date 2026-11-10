//go:build darwin

package sessionpressure

import (
	"context"
	"testing"
)

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
}
