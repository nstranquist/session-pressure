package sessionpressure

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
	"time"
)

var resourceTestNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// resourceHostFixture is a host sample. The two fixtures the acceptance
// criteria name are 16 GB with 10 logical CPUs and 48 GB with 14.
func resourceHostFixture(physicalMB float64, logicalCPUs, freePercent int, hostCPUPercent float64) Snapshot {
	return Snapshot{Timestamp: resourceTestNow, Level: LevelNormal, PhysicalMemoryMB: physicalMB, FreePercent: freePercent,
		LogicalCPUCount: logicalCPUs, HostCPUPercent: hostCPUPercent, HostCPUAvailable: true}
}

func resourceHeadroomFixture(snapshot Snapshot, leases ...WorkLeaseStatus) ResourceHeadroom {
	return computeResourceHeadroom(snapshot, true, leases, buildPeakRSSHistory(nil), resourceTestNow)
}

func TestResourceDecisionLadder(t *testing.T) {
	roomy := resourceHostFixture(49152, 14, 60, 20)
	busyCPU := resourceHostFixture(49152, 14, 60, 80)
	fullCPU := resourceHostFixture(49152, 14, 60, 99)
	lowMemory := resourceHostFixture(49152, 14, 14, 20)
	critical := lowMemory
	critical.Level = LevelCritical
	red := lowMemory
	red.Level = LevelRed
	redRoomy := roomy
	redRoomy.Level = LevelRed
	thrash := roomy
	thrash.SwapThrashLatched = true
	externalThrash := thrash
	externalThrash.SwapThrashCause = SwapThrashCauseExternal
	reclaimable := lowMemory
	reclaimable.MemoryFamilies = []MemoryFamily{{Family: "lsp", Relief: "restart_on_demand", RSSSumMB: 6000, ReclaimableMB: 6000}}
	diskHigh := roomy
	diskHigh.DiskWrite = &DiskWriteSummary{State: DiskWriteStateHigh}
	warning := roomy
	warning.Level = LevelWarning
	stale := roomy
	stale.Timestamp = resourceTestNow.Add(-time.Hour)

	job := ResourceRequest{PredictedPeakMB: 2000, PredictedCores: 4, RunningLeases: 1}
	with := func(change func(*ResourceRequest)) ResourceRequest {
		request := job
		change(&request)
		return request
	}
	cases := []struct {
		name     string
		snapshot Snapshot
		request  ResourceRequest
		label    string
		cores    int
		io       bool
	}{
		{"fits", roomy, job, "admit:none:fits", 0, false},
		{"stale sample fails open", stale, job, "admit:sample:no_sample", 0, false},
		{"partial cpu throttles to the headroom", busyCPU, job, "throttle:cpu:partial_headroom", 1, false},
		{"no cpu slows work to one core, never queues", fullCPU, job, "throttle:cpu:cpu_saturated", 1, false},
		{"no cpu, portable goes remote", fullCPU, with(func(r *ResourceRequest) { r.RemoteEligible = true }), "remote:cpu:portable", 0, false},
		{"no cpu, empty queue keeps progress", fullCPU, with(func(r *ResourceRequest) { r.RunningLeases = 0 }), "throttle:cpu:keep_progress", 1, false},
		{"no memory with other work queues", lowMemory, job, "queue:memory:no_headroom", 0, false},
		{"no memory, empty queue keeps progress", lowMemory, with(func(r *ResourceRequest) { r.RunningLeases = 0 }), "throttle:memory:keep_progress", 1, false},
		{"no memory, reclaimable family is relieved first", reclaimable, job, "relieve:memory:reclaimable_family", 0, false},
		{"no memory, portable goes remote before relief", reclaimable, with(func(r *ResourceRequest) { r.RemoteEligible = true }), "remote:memory:portable", 0, false},
		{"swap thrash queues", thrash, job, "queue:swap:swap_thrash", 0, false},
		{"critical queues", critical, job, "queue:level:critical", 0, false},
		{"critical, empty queue keeps progress", critical, with(func(r *ResourceRequest) { r.RunningLeases = 0 }), "throttle:level:keep_progress", 1, false},
		{"swap thrash, empty queue keeps progress", thrash, with(func(r *ResourceRequest) { r.RunningLeases = 0 }), "throttle:swap:keep_progress", 1, false},
		{"external thrash does not queue agent work", externalThrash, job, "throttle:swap:external_thrash", 4, true},
		{"external thrash still needs the memory fit", func() Snapshot {
			s := lowMemory
			s.SwapThrashLatched, s.SwapThrashCause = true, SwapThrashCauseExternal
			return s
		}(), job, "queue:memory:no_headroom", 0, false},
		{"red without memory headroom queues on memory", red, job, "queue:memory:no_headroom", 0, false},
		{"red alone does not delay work that fits", redRoomy, job, "admit:none:fits", 0, false},
		{"red lane admits light interactive work that would wait", red, with(func(r *ResourceRequest) { r.Interactive, r.Weight, r.FastLaneMaxWeight = true, 2, 2 }), "admit:level:red_lane", 0, false},
		{"high disk writes lower io priority", diskHigh, job, "throttle:io:disk_write_high", 4, true},
		{"fair share halves the dominant session under warning", warning, with(func(r *ResourceRequest) { r.SessionShare, r.SessionLeases = 0.8, 3 }), "throttle:cpu:fair_share", 2, false},
		{"fair share spares interactive work", warning, with(func(r *ResourceRequest) { r.SessionShare, r.SessionLeases, r.Interactive = 0.8, 3, true }), "admit:none:fits", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := decideResourceAdmission(resourceHeadroomFixture(tc.snapshot), tc.request)
			if decision.Label() != tc.label || decision.ThrottleCores != tc.cores || decision.BackgroundIO != tc.io {
				t.Fatalf("decision = %s cores=%d io=%t, want %s cores=%d io=%t (%s)", decision.Label(), decision.ThrottleCores, decision.BackgroundIO, tc.label, tc.cores, tc.io, decision.Detail())
			}
			if !validShadowResourceDecision(decision.Label()) {
				t.Fatalf("label %q is outside the closed vocabulary", decision.Label())
			}
			if throttle := decision.Throttle(); throttle.Active() != (decision.Step == ResourceStepThrottle) {
				t.Fatalf("throttle %+v does not match step %s", throttle, decision.Step)
			}
		})
	}
}

// The same rule must be sound on a 16 GB and a 48 GB host without any
// host-specific setting: reserves and headroom scale with the host.
func TestResourceHeadroomScalesWithHostSize(t *testing.T) {
	small := resourceHeadroomFixture(resourceHostFixture(16384, 10, 40, 30))
	large := resourceHeadroomFixture(resourceHostFixture(49152, 14, 40, 30))
	if small.Memory.ReserveMB != 2048 || large.Memory.ReserveMB != 6144 {
		t.Fatalf("memory reserves = %d and %d, want physical/8", small.Memory.ReserveMB, large.Memory.ReserveMB)
	}
	if small.CPU.ReserveCores != 1.3 || large.CPU.ReserveCores != 1.8 {
		t.Fatalf("cpu reserves = %.1f and %.1f, want logical/8", small.CPU.ReserveCores, large.CPU.ReserveCores)
	}
	if small.Memory.HeadroomMB != 4505 || large.Memory.HeadroomMB != 13516 {
		t.Fatalf("memory headroom = %d and %d", small.Memory.HeadroomMB, large.Memory.HeadroomMB)
	}
	if small.CPU.HeadroomCores != 5.7 || large.CPU.HeadroomCores != 8 {
		t.Fatalf("cpu headroom = %.1f and %.1f", small.CPU.HeadroomCores, large.CPU.HeadroomCores)
	}
	// One heavy job: it does not fit the small host beside other work and
	// fits the large one. One light job fits both.
	heavy := ResourceRequest{PredictedPeakMB: 6000, PredictedCores: 6, RunningLeases: 1}
	light := ResourceRequest{PredictedPeakMB: 500, PredictedCores: 1, RunningLeases: 1}
	if got := decideResourceAdmission(small, heavy).Label(); got != "queue:memory:no_headroom" {
		t.Fatalf("heavy job on 16 GB = %s", got)
	}
	if got := decideResourceAdmission(large, heavy).Label(); got != "admit:none:fits" {
		t.Fatalf("heavy job on 48 GB = %s", got)
	}
	for name, headroom := range map[string]ResourceHeadroom{"16 GB": small, "48 GB": large} {
		if got := decideResourceAdmission(headroom, light).Label(); got != "admit:none:fits" {
			t.Fatalf("light job on %s = %s", name, got)
		}
	}
	// A tiny host still keeps one whole core in reserve.
	if tiny := resourceHeadroomFixture(resourceHostFixture(8192, 4, 40, 0)); tiny.CPU.ReserveCores != 1 {
		t.Fatalf("4-CPU reserve = %.1f, want at least 1", tiny.CPU.ReserveCores)
	}
}

// Guard (WORK-1065): no fixed session count or slot count decides resource
// admission. The decision depends on how much is running only through
// measured headroom and the "nothing else runs" progress rule.
func TestResourceAdmissionHasNoFixedCount(t *testing.T) {
	roomy := resourceHeadroomFixture(resourceHostFixture(49152, 14, 60, 20))
	full := resourceHeadroomFixture(resourceHostFixture(49152, 14, 60, 99))
	job := ResourceRequest{PredictedPeakMB: 2000, PredictedCores: 4}
	for _, running := range []int{1, 2, 7, 12, 13, 50, 500} {
		for _, sessionLeases := range []int{0, 1, running} {
			request := job
			request.RunningLeases, request.SessionLeases, request.SessionShare = running, sessionLeases, 0.9
			if got := decideResourceAdmission(roomy, request).Label(); got != "admit:none:fits" {
				t.Fatalf("with headroom, %d running leases (%d of this session) gave %s; a count must not refuse work", running, sessionLeases, got)
			}
			if got := decideResourceAdmission(full, request).Step; got == ResourceStepAdmit {
				t.Fatalf("without cpu headroom, %d running leases gave a plain admit", running)
			}
		}
	}

	// Structure: the decision files must not read the weighted-capacity
	// policy. If a later change needs one of these names, it has put a fixed
	// count back into the decision.
	banned := []string{"Capacity", "WarningCapacity", "SwapThrashCapacity", "QueueDepthBlock", "WorkLimits", "limits",
		"TestWeight", "BuildWeight", "HeavyWeight", "BenchmarkWeight", "BrowserWeight", "EmulatorWeight"}
	for _, file := range []string{"work_resource_headroom.go"} {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if ident, ok := node.(*ast.Ident); ok && slices.Contains(banned, ident.Name) {
				t.Errorf("%s refers to %s: resource admission must not read weighted capacity or class weights", file, ident.Name)
			}
			return true
		})
		// Integer literals above 1 in the decision would be a count or a
		// threshold. The file's named constants are scale factors and
		// percentiles, declared outside decideResourceAdmission.
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Name.Name != "decideResourceAdmission" {
				continue
			}
			ast.Inspect(function, func(node ast.Node) bool {
				if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.INT && literal.Value != "0" && literal.Value != "1" && literal.Value != "2" {
					t.Errorf("decideResourceAdmission holds the literal %s; name it and state what it scales with", literal.Value)
				}
				return true
			})
		}
	}
}

// A slow ramp in one dimension walks the ladder in order. It never jumps
// from a plain admit to a refusal, and nothing is asked to stop.
func TestResourceRampsWalkTheLadder(t *testing.T) {
	rank := map[string]int{ResourceStepAdmit: 0, ResourceStepThrottle: 1, ResourceStepRemote: 2, ResourceStepRelieve: 2, ResourceStepQueue: 3}
	job := ResourceRequest{PredictedPeakMB: 3000, PredictedCores: 6, RunningLeases: 2}
	walk := func(name, last string, samples []Snapshot) {
		previous, seen := -1, map[string]bool{}
		for _, snapshot := range samples {
			decision := decideResourceAdmission(resourceHeadroomFixture(snapshot), job)
			if !validShadowResourceDecision(decision.Label()) {
				t.Fatalf("%s: label %q is outside the closed vocabulary", name, decision.Label())
			}
			current := rank[decision.Step]
			if current < previous {
				t.Fatalf("%s: step went back from rank %d to %s as pressure rose", name, previous, decision.Label())
			}
			previous = current
			seen[decision.Step] = true
		}
		if !seen[ResourceStepAdmit] || !seen[last] {
			t.Fatalf("%s: ramp did not go from admit to %s: %v", name, last, seen)
		}
	}
	var cpuRamp, memoryRamp []Snapshot
	for percent := 0.0; percent <= 100; percent += 2 {
		cpuRamp = append(cpuRamp, resourceHostFixture(49152, 14, 60, percent))
	}
	for free := 60; free >= 0; free-- {
		memoryRamp = append(memoryRamp, resourceHostFixture(49152, 14, free, 10))
	}
	// CPU pressure ends at a throttle: it slows work and never queues it.
	// Memory pressure ends at the queue: it can thrash the host.
	walk("cpu", ResourceStepThrottle, cpuRamp)
	walk("memory", ResourceStepQueue, memoryRamp)
	for _, snapshot := range cpuRamp {
		if step := decideResourceAdmission(resourceHeadroomFixture(snapshot), job).Step; step == ResourceStepQueue {
			t.Fatalf("cpu ramp queued work at %.0f%% host CPU", snapshot.HostCPUPercent)
		}
	}
}

func resourceCompleted(class WorkClass, key string, cpuMS, runtimeMS int64, throttleCores int) WorkEvent {
	return WorkEvent{Event: WorkEventCompleted, Class: class, PlacementKey: key, CommandCPUAvailable: true,
		CommandCPUMilliseconds: cpuMS, RuntimeMillis: runtimeMS, ThrottleCores: throttleCores}
}

func TestCoreHistoryPredictsFromMeasuredRuns(t *testing.T) {
	history := newCoreHistory()
	if cores, source, samples := history.predict(WorkClassTest, "", "key"); cores != resourceFallbackCores || source != predictionFallback || samples != 0 {
		t.Fatalf("empty history = %.1f %s %d", cores, source, samples)
	}
	for _, cores := range []int64{2, 4, 6} {
		history.add(resourceCompleted(WorkClassTest, "key", cores*10_000, 10_000, 0))
	}
	// Throttled, unmeasured and very short runs must not teach the model.
	history.add(resourceCompleted(WorkClassTest, "key", 10_000, 10_000, 2))
	history.add(resourceCompleted(WorkClassTest, "key", 900, 50, 0))
	history.add(WorkEvent{Event: WorkEventCompleted, Class: WorkClassTest, PlacementKey: "key", RuntimeMillis: 10_000})
	if cores, source, samples := history.predict(WorkClassTest, "", "key"); cores != 4 || source != predictionByKey || samples != 3 {
		t.Fatalf("key prediction = %.1f %s %d, want the median of 2,4,6", cores, source, samples)
	}
	// Three class samples are below the class minimum: another command of
	// the class still gets the conservative fallback.
	if cores, source, _ := history.predict(WorkClassTest, "", "other"); cores != resourceFallbackCores || source != predictionFallback {
		t.Fatalf("class prediction below the minimum = %.1f %s", cores, source)
	}
}

func TestResourceHeadroomChargesRampingLeases(t *testing.T) {
	snapshot := resourceHostFixture(49152, 14, 60, 20)
	fresh := WorkLeaseStatus{OperationID: "a", Class: WorkClassBuild, StartedAt: resourceTestNow.Add(-10 * time.Second)}
	settled := WorkLeaseStatus{OperationID: "b", Class: WorkClassBuild, StartedAt: resourceTestNow.Add(-10 * time.Minute)}
	parked := WorkLeaseStatus{OperationID: "c", Class: WorkClassBuild, StartedAt: resourceTestNow.Add(-10 * time.Second), Parked: true}
	headroom := resourceHeadroomFixture(snapshot, fresh, settled, parked)
	if headroom.CPU.RampingLeaseCount != 1 || headroom.CPU.InflightCores != resourceFallbackCores {
		t.Fatalf("in-flight = %d leases, %.1f cores; want only the fresh lease charged", headroom.CPU.RampingLeaseCount, headroom.CPU.InflightCores)
	}
	if want := roundTenth(14*0.8 - 1.8 - resourceFallbackCores); headroom.CPU.HeadroomCores != want {
		t.Fatalf("cpu headroom = %.1f, want %.1f", headroom.CPU.HeadroomCores, want)
	}
}

func TestSessionCostShareUsesLeasesNotTrees(t *testing.T) {
	leases := []WorkLeaseStatus{
		{OperationID: "a", Class: WorkClassBuild, PredictedPeakMB: 3000},
		{OperationID: "b", Class: WorkClassBuild, PredictedPeakMB: 3000},
		{OperationID: "c", Class: WorkClassTest, PredictedPeakMB: 2000},
		{OperationID: "d", Class: WorkClassTest, PredictedPeakMB: 9000, Parked: true},
	}
	owners := map[string]string{"a": "s1", "b": "s1", "c": "s2", "d": "s2"}
	history := buildPeakRSSHistory(nil)
	share, count := sessionCostShare(leases, owners, "s1", history)
	if count != 2 || share < 0.70 || share > 0.72 {
		t.Fatalf("s1 share = %.3f over %d leases, want about 0.71 over 2", share, count)
	}
	if share, count := sessionCostShare(leases, owners, "", history); share != 0 || count != 0 {
		t.Fatalf("unknown session share = %.3f over %d", share, count)
	}
	if share, count := sessionCostShare(leases, owners, "s3", history); share != 0 || count != 0 {
		t.Fatalf("absent session share = %.3f over %d", share, count)
	}
}

func TestShadowResourceDecisionVocabularyIsClosed(t *testing.T) {
	for _, label := range []string{"", "admit:none:fits", "throttle:cpu:partial_headroom", "throttle:cpu:cpu_saturated", "queue:swap:swap_thrash", "queue:level:red"} {
		if !validShadowResourceDecision(label) {
			t.Errorf("%q should be valid", label)
		}
	}
	for _, label := range []string{"admit", "admit:none", "kill:memory:no_headroom", "stop:cpu:fits", "admit:tree:fits", "admit:none:because", "admit:none:fits:extra"} {
		if validShadowResourceDecision(label) {
			t.Errorf("%q should be refused", label)
		}
	}
	for _, step := range resourceSteps {
		if strings.Contains(step, "kill") || strings.Contains(step, "stop") || strings.Contains(step, "cancel") {
			t.Errorf("ladder step %q would end a run", step)
		}
	}
}

func TestShadowResourceDecisionFitsTheWorkEvent(t *testing.T) {
	var event WorkEvent
	decision := decideResourceAdmission(resourceHeadroomFixture(resourceHostFixture(49152, 14, 60, 80)), ResourceRequest{PredictedPeakMB: 2000, PredictedCores: 4, RunningLeases: 1})
	decision.apply(&event)
	if event.ShadowResourceDecision != "throttle:cpu:partial_headroom" || event.ShadowResourceRule != ResourceHeadroomRuleVersion ||
		event.ShadowPredictedCores != 4 || event.ShadowThrottleCores != 1 || event.ShadowHeadroomCores != 1 {
		t.Fatalf("event fields = %+v", event)
	}
	var untouched WorkEvent
	ShadowResourceDecision{}.apply(&untouched)
	if untouched.ShadowResourceDecision != "" || untouched.ShadowResourceRule != "" {
		t.Fatal("an empty decision must leave the event unchanged")
	}
}
