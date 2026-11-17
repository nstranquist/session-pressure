package sessionpressure

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Resource admission (WorkLimits.Admission = resource) is live without
// enforce_admission, and the weighted unit cap no longer decides starts.

func TestResourceAdmissionIsTheDefaultAndRemovesTheUnitCap(t *testing.T) {
	policy := DefaultPolicy(36 * 1024)
	if !policy.WorkLimits.ResourceAdmission() {
		t.Fatalf("default admission = %q, want resource", policy.WorkLimits.Admission)
	}
	policy.Enabled, policy.EnforceAdmission = true, false
	if !WorkResourceAdmissionLive(policy) {
		t.Fatal("resource admission needs enforce_admission")
	}
	limits := policy.WorkLimits.admissionLimits()
	if limits.Capacity != unboundedWorkCapacity || limits.WarningCapacityEnabled {
		t.Fatalf("resource admission kept a unit cap: capacity=%d warning=%v", limits.Capacity, limits.WarningCapacityEnabled)
	}
	// The exclusive benchmark class still runs alone.
	if weight, err := limits.Weight(WorkClassBenchmarkExclusive); err != nil || weight != limits.Capacity {
		t.Fatalf("exclusive weight = %d, %v", weight, err)
	}
	weighted := policy.WorkLimits
	weighted.Admission = WorkAdmissionWeighted
	if got := weighted.admissionLimits(); got.Capacity != normalizeWorkLimits(weighted).Capacity {
		t.Fatalf("weighted admission changed the cap: %d", got.Capacity)
	}
}

func TestResourceCoordinatorAdmitsPastTheConfiguredCap(t *testing.T) {
	coordinator := testWorkCoordinator(t)
	coordinator.Limits.Admission = WorkAdmissionResource
	coordinator.Limits.Capacity = 2
	ctx := context.Background()
	for range 5 {
		if _, _, err := coordinator.Acquire(ctx, WorkClassBuild); err != nil {
			t.Fatalf("resource admission refused on units: %v", err)
		}
	}
	status, err := coordinator.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Admission != WorkAdmissionResource || len(status.Leases) != 5 {
		t.Fatalf("status admission=%q leases=%d", status.Admission, len(status.Leases))
	}
}

func resourceFitSampleFixture(freePercent int, at time.Time) workResourceFitSample {
	return workResourceFitSample{armed: true, ok: true, snapshot: Snapshot{
		Timestamp: at, Level: LevelNormal, PhysicalMemoryMB: 49152, FreePercent: freePercent,
	}}
}

func TestResourceFitUnderTheStateLock(t *testing.T) {
	now := resourceTestNow
	running := []WorkLeaseRecord{{Class: WorkClassBuild, StartedAt: now.Add(-30 * time.Second), PredictedPeakMB: 4000}}
	start := WorkLeaseRecord{Class: WorkClassBuild, PredictedPeakMB: 3000}
	// 48 GB at 20% free: 9.8 GB available, 6 GB reserve, 4 GB ramping.
	tight := resourceFitSampleFixture(20, now)
	if err := resourceFit(running, start, tight, now); !errors.Is(err, ErrWorkResource) {
		t.Fatalf("over-headroom start was admitted: %v", err)
	}
	for name, test := range map[string]struct {
		leases []WorkLeaseRecord
		record WorkLeaseRecord
		sample workResourceFitSample
	}{
		"fits":                            {running, start, resourceFitSampleFixture(60, now)},
		"empty host never waits":          {nil, start, tight},
		"stale sample fails open":         {running, start, resourceFitSampleFixture(20, now.Add(-time.Hour))},
		"missing sample fails open":       {running, start, workResourceFitSample{armed: true}},
		"unarmed":                         {running, start, workResourceFitSample{}},
		"unpredicted start":               {running, WorkLeaseRecord{Class: WorkClassBuild}, tight},
		"a ramped lease is in the sample": {[]WorkLeaseRecord{{Class: WorkClassBuild, StartedAt: now.Add(-time.Hour), PredictedPeakMB: 4000}}, start, tight},
	} {
		if err := resourceFit(test.leases, test.record, test.sample, now); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestResourceFitErrorIsRetriedAsAQueueBlocker(t *testing.T) {
	if !isQueueBlocker(WorkBlockerResource) {
		t.Fatal("resource blocker is not a queue blocker")
	}
	event := validWorkEvent(resourceTestNow, WorkEventQueued)
	event.EventID = workEventID(event.OperationID, event.Event, event.LeaseID)
	event.Blocker = WorkBlockerResource
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
}

func liveResourceGate(decision ShadowResourceDecision, ok bool) *workAdmissionGate {
	limits := testWorkLimits()
	limits.Admission = WorkAdmissionResource
	return &workAdmissionGate{
		limits: limits, class: WorkClassBuild,
		memoryGuardsActive: func() bool { return true },
		resourceHeadroom:   func(Admission) (ShadowResourceDecision, bool) { return decision, ok },
	}
}

func TestLiveResourceGateHoldsOnlyQueueAndRelieve(t *testing.T) {
	allowed := Admission{Allowed: true, Level: LevelNormal}
	for _, test := range []struct {
		decision ShadowResourceDecision
		ok       bool
		held     bool
		dim      string
	}{
		{ShadowResourceDecision{Step: ResourceStepQueue, Dimension: ResourceDimensionMemory, Reason: resourceReasonNoRoom}, true, true, "memory"},
		{ShadowResourceDecision{Step: ResourceStepQueue, Dimension: ResourceDimensionLevel, Reason: resourceReasonCritical}, true, true, "memory"},
		{ShadowResourceDecision{Step: ResourceStepQueue, Dimension: ResourceDimensionSwap, Reason: resourceReasonThrash}, true, true, "swap"},
		{ShadowResourceDecision{Step: ResourceStepRelieve, Dimension: ResourceDimensionMemory, Reason: resourceReasonReclaim}, true, true, "memory"},
		{ShadowResourceDecision{Step: ResourceStepThrottle, Dimension: ResourceDimensionCPU, Reason: resourceReasonSaturated, ThrottleCores: 1}, true, false, ""},
		{ShadowResourceDecision{Step: ResourceStepAdmit, Dimension: ResourceDimensionNone, Reason: resourceReasonFits}, true, false, ""},
		{ShadowResourceDecision{}, false, false, ""},
	} {
		gate := liveResourceGate(test.decision, test.ok)
		decision, held := gate.resourceAdmissionDecision(allowed, false)
		if held != test.held || (held && (decision.Allowed || decision.Dimension != test.dim || !strings.HasPrefix(decision.Reason, "resource headroom: "))) {
			t.Fatalf("%s: held=%v decision=%+v", test.decision.Label(), held, decision)
		}
		if held && gate.decisionLabel() != ResourceDeferredDecision {
			t.Fatalf("label = %q", gate.decisionLabel())
		}
		// A lease the process owns is never held.
		if _, held := liveResourceGate(test.decision, test.ok).resourceAdmissionDecision(allowed, true); held {
			t.Fatal("owned lease was held")
		}
	}
	// Disabled policy and weighted admission leave the gate off.
	gate := liveResourceGate(ShadowResourceDecision{Step: ResourceStepQueue, Dimension: ResourceDimensionMemory, Reason: resourceReasonNoRoom}, true)
	gate.memoryGuardsActive = func() bool { return false }
	if _, held := gate.resourceAdmissionDecision(allowed, false); held {
		t.Fatal("disabled policy held work")
	}
	gate = liveResourceGate(ShadowResourceDecision{Step: ResourceStepQueue, Dimension: ResourceDimensionMemory, Reason: resourceReasonNoRoom}, true)
	gate.limits.Admission = WorkAdmissionWeighted
	if _, held := gate.resourceAdmissionDecision(allowed, false); held {
		t.Fatal("weighted admission held work on resources")
	}
}

func TestLiveResourceThrottleAppliesOnlyBackgroundIO(t *testing.T) {
	allowed := Admission{Allowed: true, Level: LevelNormal}
	gate := liveResourceGate(ShadowResourceDecision{Step: ResourceStepThrottle, Dimension: ResourceDimensionIO, Reason: resourceReasonDiskWrite, ThrottleCores: 4, BackgroundIO: true}, true)
	gate.resourceAdmissionDecision(allowed, false)
	if throttle := gate.liveResourceThrottle(); throttle.Cores != 0 || !throttle.BackgroundIO {
		t.Fatalf("live throttle = %+v, want background I/O only", throttle)
	}
	gate = liveResourceGate(ShadowResourceDecision{Step: ResourceStepThrottle, Dimension: ResourceDimensionCPU, Reason: resourceReasonSaturated, ThrottleCores: 1}, true)
	gate.resourceAdmissionDecision(allowed, false)
	if throttle := gate.liveResourceThrottle(); throttle.Active() {
		t.Fatalf("CPU throttle applied live: %+v", throttle)
	}
}

// Under resource admission the ladder owns swap thrash and memory; the
// older guards stand down so one start is not judged twice.
func TestResourceAdmissionStandsDownTheOlderGuards(t *testing.T) {
	gate := liveResourceGate(ShadowResourceDecision{Step: ResourceStepAdmit, Dimension: ResourceDimensionNone, Reason: resourceReasonFits}, true)
	gate.limits.MemoryAdmission = MemoryAdmissionEnforce
	gate.swapThrashEnforced = true
	gate.warningCapacityStatus = func() (WorkStatus, bool) { return WorkStatus{Used: 100, Capacity: 8}, true }
	if gate.memoryGuardsEnforced() {
		t.Fatal("memory guard still armed under resource admission")
	}
	if _, held := gate.swapThrashDecision(swapThrashAdmission(true), false); held {
		t.Fatal("swap-thrash serialization still armed under resource admission")
	}
}

func TestLiveResourceDecisionFitsTheWorkEvent(t *testing.T) {
	event := validWorkEvent(resourceTestNow, WorkEventQueued)
	event.EventID = workEventID(event.OperationID, event.Event, event.LeaseID)
	event.ResourceDecision = "throttle:io:disk_write_high"
	event.ResourceRule = ResourceHeadroomRuleVersion
	event.ThrottleBackgroundIO = true
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	event.ResourceDecision = "admit:none:made_up"
	if err := event.Validate(); err == nil {
		t.Fatal("open vocabulary accepted")
	}
	// Shadow rows from the v2 install stay readable.
	old := validWorkEvent(resourceTestNow, WorkEventQueued)
	old.EventID = workEventID(old.OperationID, old.Event, old.LeaseID)
	old.ShadowResourceDecision, old.ShadowResourceRule = "throttle:cpu:cpu_saturated", resourceHeadroomRuleVersionV2
	if err := old.Validate(); err != nil {
		t.Fatalf("v2 row rejected: %v", err)
	}
}

func TestExportedResourceLadderMatchesTheInternalOne(t *testing.T) {
	headroom := resourceHeadroomFixture(resourceHostFixture(49152, 14, 14, 20))
	request := ResourceRequest{PredictedPeakMB: 2000, PredictedCores: 4, RunningLeases: 1}
	if got, want := DecideResourceAdmission(headroom, request).Label(), decideResourceAdmission(headroom, request).Label(); got != want {
		t.Fatalf("exported %s, internal %s", got, want)
	}
	if _, err := ReadResourceHeadroom(context.Background(), t.TempDir(), nil, resourceTestNow); err == nil {
		t.Fatal("nil coordinator accepted")
	}
	coordinator := testWorkCoordinator(t)
	headroomView, err := ReadResourceHeadroom(context.Background(), coordinator.Dir, coordinator, resourceTestNow)
	if err != nil || headroomView.Rule != ResourceHeadroomRuleVersion {
		t.Fatalf("read headroom = %+v, %v", headroomView.Rule, err)
	}
}

func TestDoctorNamesResourceAdmission(t *testing.T) {
	policy := DefaultPolicy(36 * 1024)
	policy.Enabled = true
	in := PressureDoctorInput{
		Now: time.Now(), Policy: policy, HasLatest: true,
		Latest: Snapshot{SwapIOAvailable: true, SwapThrashLatched: true, SwapOutMBPerSecond: 80, SwapThrashCause: "agent"},
	}
	if got := strings.Join(swapThrashDoctorWarnings(in), "\n"); !strings.Contains(got, "resource admission") {
		t.Fatalf("resource warning = %q", got)
	}
}

func TestStallGuardUnderResourceAdmission(t *testing.T) {
	now := resourceTestNow
	status := WorkStatus{
		Leases:  []WorkLeaseStatus{{ID: "a", Class: WorkClassBuild, StartedAt: now.Add(-30 * time.Second), PredictedPeakMB: 4000}},
		Waiters: []WorkWaiterStatus{{OperationID: "w", Class: WorkClassBuild, PredictedPeakMB: 3000, HeartbeatAt: now}},
	}
	allowed := Admission{Allowed: true, Level: LevelNormal}
	leases := []WorkLeaseRecord{{Class: WorkClassBuild, StartedAt: now.Add(-30 * time.Second), PredictedPeakMB: 4000}}
	fitsAt := func(free int) func(WorkWaiterStatus) bool {
		sample := resourceFitSampleFixture(free, now)
		return func(waiter WorkWaiterStatus) bool {
			return resourceFit(leases, WorkLeaseRecord{Class: waiter.Class, PredictedPeakMB: waiter.PredictedPeakMB}, sample, now) == nil
		}
	}
	// Unbounded capacity alone would call this a stall.
	if _, stalled := stallCandidate(status, allowed, unboundedWorkCapacity, now, fitsAt(20)); stalled {
		t.Fatal("a waiter that waits for memory was called a stall")
	}
	if _, stalled := stallCandidate(status, allowed, unboundedWorkCapacity, now, fitsAt(60)); !stalled {
		t.Fatal("a waiter that fits memory and is not served is a stall")
	}
}
