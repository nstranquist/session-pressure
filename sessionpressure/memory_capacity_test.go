package sessionpressure

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTreeRSSKBSumsDescendantsOnly(t *testing.T) {
	rows := []processRow{
		{pid: 1, ppid: 0, rssKB: 999},
		{pid: 10, ppid: 1, rssKB: 100}, // root
		{pid: 11, ppid: 10, rssKB: 200},
		{pid: 12, ppid: 11, rssKB: 300},
		{pid: 13, ppid: 10, rssKB: 400},
		{pid: 20, ppid: 1, rssKB: 5000}, // sibling, not ours
	}
	if kb, count := treeRSSKB(rows, 10, nil); kb != 1000 || count != 4 {
		t.Fatalf("tree = %d KB over %d", kb, count)
	}
	if _, count := treeRSSKB(rows, 99, nil); count != 0 {
		t.Fatal("missing root attributed")
	}
	// rssOf replaces row memory for tree members only.
	asked := map[int]bool{}
	kb, _ := treeRSSKB(rows, 10, func(pid int) (int64, bool) { asked[pid] = true; return 1, pid != 13 })
	if kb != 403 || asked[20] || asked[1] {
		t.Fatalf("rssOf: %d KB, asked %v", kb, asked)
	}
	// A ppid cycle cannot loop forever.
	if _, count := treeRSSKB([]processRow{{pid: 5, ppid: 6}, {pid: 6, ppid: 5}}, 5, nil); count != 2 {
		t.Fatalf("cycle count %d", count)
	}
}

func TestTreePeakSamplerSeesAChildTree(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no process listing")
	}
	// The tree outlives the test's wait; the test kills it.
	command := exec.Command("sh", "-c", "sleep 600 & sleep 600; wait")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// Stop after two completed listings, not after a fixed sleep: under load
	// one process listing can take longer than the sleep did.
	listed := make(chan struct{}, 64)
	list := func() ([]processRow, error) {
		rows, err := listProcessRows()
		select {
		case listed <- struct{}{}:
		default:
		}
		return rows, err
	}
	sampler := startTreePeakSampler(command.Process.Pid, time.Millisecond, list, processRSSKB)
	for range 3 {
		select {
		case <-listed:
		case <-time.After(testWatchdog):
			t.Fatal("tree sampler did not list processes")
		}
	}
	peak, samples := sampler.Stop()
	_ = command.Process.Kill()
	_ = command.Wait()
	if samples < 2 || peak <= 0 {
		t.Fatalf("peak %.1f MB from %d samples", peak, samples)
	}
	if again, _ := sampler.Stop(); again != peak {
		t.Fatal("second Stop changed the result")
	}
	var none *treePeakSampler
	if mb, n := none.Stop(); mb != 0 || n != 0 {
		t.Fatal("nil sampler")
	}
}

func TestHeadroomPredictorUsesTheTreePeak(t *testing.T) {
	var events []WorkEvent
	for range 5 {
		event := completedPeak(WorkClassTest, "", "", 300)
		event.CommandTreePeakRSSMB = 2400 // parallel test binaries
		events = append(events, event)
	}
	if mb, source := buildPeakRSSHistory(events).predict(WorkClassTest, "", ""); mb != 2400 || source != predictionByClass {
		t.Fatalf("prediction %d %s", mb, source)
	}
	// A wrapper reporting ~1 MB by wait4 counts once its tree is seen.
	wrapped := completedPeak(WorkClassBuild, "", "", 1)
	wrapped.CommandTreePeakRSSMB = 900
	if got := commandMemoryPeakMB(wrapped); got != 900 {
		t.Fatalf("wrapped peak %v", got)
	}
}

func TestMemoryHeadroomHoldRules(t *testing.T) {
	noRoom := ShadowMemoryDecision{Reason: headroomNoRoom, PredictedPeakMB: 3000, PredictionSource: predictionByClass, HeadroomMB: 1200}
	if hold, reason := memoryHeadroomHold(noRoom, 3); !hold || !strings.Contains(reason, "3000 MB") || !strings.HasPrefix(reason, "memory headroom:") {
		t.Fatalf("no room with running work: %v %q", hold, reason)
	}
	for name, tc := range map[string]struct {
		decision ShadowMemoryDecision
		running  int
	}{
		"nothing running: the queue must move": {noRoom, 0},
		"fits":                                 {ShadowMemoryDecision{Admit: true, Reason: headroomFits}, 4},
		"no sample fails open":                 {ShadowMemoryDecision{Reason: headroomNoSample}, 4},
		"swap thrash belongs to the swap gate": {ShadowMemoryDecision{Reason: headroomThrash}, 4},
		"red belongs to the level gate":        {ShadowMemoryDecision{Reason: headroomRed}, 4},
	} {
		if hold, _ := memoryHeadroomHold(tc.decision, tc.running); hold {
			t.Errorf("%s: held", name)
		}
	}
}

func TestGateEnforcesMemoryHeadroomOnlyWhenConfigured(t *testing.T) {
	noRoom := func(Admission) (ShadowMemoryDecision, int, bool) {
		return ShadowMemoryDecision{Reason: headroomNoRoom, PredictedPeakMB: 3000, HeadroomMB: 100}, 2, true
	}
	allowed := Admission{Allowed: true, Level: LevelNormal}
	limits := testWorkLimits()
	shadow := &workAdmissionGate{limits: limits, memoryHeadroom: noRoom}
	if _, held := shadow.memoryHeadroomDecision(allowed, false); held {
		t.Fatal("shadow mode held work")
	}
	limits.MemoryAdmission = MemoryAdmissionEnforce
	enforced := &workAdmissionGate{limits: limits, memoryHeadroom: noRoom, memoryGuardsActive: func() bool { return true }}
	decision, held := enforced.memoryHeadroomDecision(allowed, false)
	if !held || decision.Allowed || decision.Dimension != "memory" || decision.RetryInterval <= 0 {
		t.Fatalf("enforce mode: %+v %v", decision, held)
	}
	if _, held := enforced.memoryHeadroomDecision(allowed, true); held {
		t.Fatal("held a lease this process already owns")
	}
	enforced.memoryGuardsActive = func() bool { return false }
	if _, held := enforced.memoryHeadroomDecision(allowed, false); held {
		t.Fatal("held while admission is not enforced")
	}
}

func TestMemoryAdmissionPolicyValidation(t *testing.T) {
	policy := DefaultPolicy(16384)
	for _, mode := range []string{"", MemoryAdmissionShadow, MemoryAdmissionEnforce} {
		policy.WorkLimits.MemoryAdmission = mode
		if err := policy.Validate(); err != nil {
			t.Fatalf("%q rejected: %v", mode, err)
		}
	}
	policy.WorkLimits.MemoryAdmission = "on"
	if err := policy.Validate(); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestEvaluationChecksHoldAtHigherCapacity(t *testing.T) {
	for _, capacity := range []int{8, 16, 32} {
		// The live weights (2026-11-16) at each capacity.
		policy := DefaultPolicy(16384)
		w := &policy.WorkLimits
		w.Capacity, w.ExpressTestWeight, w.TestWeight, w.ExpressBuildWeight, w.BuildWeight = capacity, 1, 2, 2, 5
		w.BrowserWeight, w.EmulatorWeight, w.HeavyWeight, w.BenchmarkWeight = 2, 5, 6, 6
		if err := policy.Validate(); err != nil {
			t.Fatalf("capacity %d: %v", capacity, err)
		}
		report := EvaluateWorkSystem(policy)
		for _, scenario := range report.Scenarios {
			if (scenario.ID == "calibrated-classes" || scenario.ID == "express-lookahead-under-benchmark") && !scenario.Passed {
				t.Errorf("capacity %d: %s failed: %s", capacity, scenario.ID, scenario.Detail)
			}
		}
	}
}
