package sessionpressure

import (
	"testing"
	"time"
)

// A build lease spawns compile, link, and test processes between inventories.
// Before new-process CPU, any such child made leased-work CPU unknown, so the
// figure was missing under exactly the load it exists to measure.
func TestAnnotateProcessCPUPercentMeasuresProcessesStartedInsideInterval(t *testing.T) {
	previousAt := time.Unix(1_800_000_000, 0)
	capturedAt := previousAt.Add(2 * time.Second)
	inside := previousAt.Add(500 * time.Millisecond).UnixNano()
	previous := []Process{{PID: 42, StartedAtNS: 100, CPUTotalNS: 1_000, CPUTotalValid: true}}
	current := []Process{
		{PID: 7, StartedAtNS: inside, CPUTotalNS: 1_000_000_000, CPUTotalValid: true},
		// A reused PID whose start is inside the interval is a new process.
		{PID: 42, StartedAtNS: inside, CPUTotalNS: 500_000_000, CPUTotalValid: true},
		// Started before previousAt but absent from the previous inventory:
		// its earlier CPU time is unknown.
		{PID: 8, StartedAtNS: previousAt.Add(-time.Second).UnixNano(), CPUTotalNS: 1_000, CPUTotalValid: true},
		// A start after the capture is a clock anomaly, not evidence.
		{PID: 9, StartedAtNS: capturedAt.Add(time.Second).UnixNano(), CPUTotalNS: 1_000, CPUTotalValid: true},
		// No cumulative counter: nothing to measure.
		{PID: 10, StartedAtNS: inside},
	}
	annotateProcessCPUPercent(current, previous, previousAt, capturedAt)
	for _, want := range []struct {
		index   int
		valid   bool
		percent float64
	}{{0, true, 50}, {1, true, 25}, {2, false, 0}, {3, false, 0}, {4, false, 0}} {
		process := current[want.index]
		if process.NewProcessCPUValid != want.valid || process.NewProcessCPUPercent != want.percent {
			t.Fatalf("pid %d: new-process CPU valid=%v percent=%v, want %v %v", process.PID, process.NewProcessCPUValid, process.NewProcessCPUPercent, want.valid, want.percent)
		}
		// Idle and relief selection keep reading a new process as unknown CPU.
		if process.CPUAvailable {
			t.Fatalf("pid %d: a new process must not become interval CPU evidence: %+v", process.PID, process)
		}
	}
}

func TestCoordinatedWorkCountsLeasedProcessesStartedBetweenInventories(t *testing.T) {
	leases := []WorkLeaseStatus{{PID: 100, Class: WorkClassBuild}}
	root := Process{PID: 100, PPID: 1, CPUAvailable: true, CPUPercent: 10}
	compile := Process{PID: 101, PPID: 100, NewProcessCPUValid: true, NewProcessCPUPercent: 190}
	work := attributeCoordinatedWork([]Process{root, compile}, leases, time.Unix(1_800_000_000, 0), 2)
	if !work.CPUAvailable || work.CPUPercent != 100 {
		t.Fatalf("leased CPU with a new compile step = available %v %.1f%%, want available 100%%", work.CPUAvailable, work.CPUPercent)
	}
	if len(work.ByClass) != 1 || !work.ByClass[0].CPUAvailable || work.ByClass[0].CPUPercent != 100 {
		t.Fatalf("build class usage: %+v", work.ByClass)
	}

	unknown := Process{PID: 102, PPID: 100}
	work = attributeCoordinatedWork([]Process{root, compile, unknown}, leases, time.Unix(1_800_000_000, 0), 2)
	if work.CPUAvailable {
		t.Fatalf("a leased process with no CPU evidence must keep the total unknown: %+v", work)
	}
}

// Relief selection signals quiescent agent trees under critical pressure. A
// tree that just spawned a process is active; new-process CPU must not make
// such a tree look measured and idle.
func TestAgentTreeKeepsNewProcessCPUUnknownForRelief(t *testing.T) {
	agent := Process{PID: 5, PPID: 1, Agent: "claude", Executable: "claude", ElapsedSeconds: 3600, CPUAvailable: true, CPUPercent: 0}
	spawned := Process{PID: 6, PPID: 5, NewProcessCPUValid: true, NewProcessCPUPercent: 0.1}
	trees := buildAgentTrees([]Process{agent, spawned})
	if len(trees) != 1 {
		t.Fatalf("trees: %+v", trees)
	}
	if trees[0].CPUAvailable {
		t.Fatalf("a tree with a new child must keep unknown CPU: %+v", trees[0])
	}
	policy := DefaultPolicy(16 * 1024)
	streak := map[int]int{5: policy.CriticalSustainSamples}
	annotateQuiescence(trees, policy, streak)
	if trees[0].QuiescentSamples != 0 {
		t.Fatalf("a tree that spawned a process must reset its quiescent streak: %+v", trees[0])
	}
}
