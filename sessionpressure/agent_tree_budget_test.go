package sessionpressure

import "testing"

// Native inventories omit commands for unrelated processes. Their number must
// not multiply catalog reload checks and path allocations in every sample.
func TestAgentTreeNativeInventoryAllocationBudget(t *testing.T) {
	processes := make([]Process, 2048)
	for i := range processes {
		processes[i] = Process{PID: i + 1, PPID: 1, Executable: "daemon", CPUAvailable: true}
	}
	processes[10].Agent = "codex"
	allocations := testing.AllocsPerRun(5, func() {
		trees := buildAgentTrees(processes)
		if len(trees) != 1 || trees[0].RootPID != 11 {
			t.Fatalf("lost native agent root: %+v", trees)
		}
	})
	if allocations > 4096 {
		t.Fatalf("native inventory allocated %.0f objects; budget 4096", allocations)
	}
}
