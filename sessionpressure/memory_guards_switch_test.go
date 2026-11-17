package sessionpressure

import (
	"strings"
	"testing"
	"time"
)

// The memory-headroom and swap-thrash guards have their own switch
// (work_limits.memory_admission = enforce). They must act with
// enforce_admission off, which is how Nico runs the Mac since 2026-11-15.
func TestMemoryGuardsSwitchIsIndependentOfEnforceAdmission(t *testing.T) {
	for _, test := range []struct {
		name                       string
		enabled, enforce           bool
		memoryAdmission            string
		wantMemory, wantSwapThrash bool
	}{
		{"observe, shadow", true, false, MemoryAdmissionShadow, false, false},
		{"observe, unset", true, false, "", false, false},
		{"observe, enforce memory", true, false, MemoryAdmissionEnforce, true, true},
		{"enforcing profile, shadow", true, true, MemoryAdmissionShadow, false, true},
		{"enforcing profile, enforce memory", true, true, MemoryAdmissionEnforce, true, true},
		{"disabled policy", false, true, MemoryAdmissionEnforce, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := DefaultPolicy(36 * 1024)
			policy.Enabled = test.enabled
			policy.EnforceAdmission = test.enforce
			policy.WorkLimits.MemoryAdmission = test.memoryAdmission
			if got := WorkMemoryAdmissionEnforced(policy); got != test.wantMemory {
				t.Fatalf("memory admission enforced=%v want %v", got, test.wantMemory)
			}
			if got := WorkSwapThrashEnforced(policy); got != test.wantSwapThrash {
				t.Fatalf("swap thrash enforced=%v want %v", got, test.wantSwapThrash)
			}
		})
	}
}

func TestPersistedObservePolicyArmsMemoryGuardsWithoutHostBlocking(t *testing.T) {
	dir := t.TempDir()
	if configuredWorkPolicyEnabled(dir) {
		t.Fatal("missing policy file armed the memory guards")
	}
	policy, err := ApplyPolicyProfile(DefaultPolicy(36*1024), PolicyProfileObserve, ApplyProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	policy.WorkLimits.MemoryAdmission = MemoryAdmissionEnforce
	if err := SavePolicy(PolicyPath(dir), policy); err != nil {
		t.Fatal(err)
	}
	if !configuredWorkPolicyEnabled(dir) {
		t.Fatal("enabled observe policy did not arm the memory guards")
	}
	if configuredWorkHostAdmissionEnforced(dir) {
		t.Fatal("memory_admission=enforce turned on host admission")
	}
	// Re-applying observe keeps the operator's memory switch.
	again, err := ApplyPolicyProfile(policy, PolicyProfileObserve, ApplyProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if again.WorkLimits.MemoryAdmission != MemoryAdmissionEnforce || again.EnforceAdmission {
		t.Fatalf("observe re-apply changed the switch: memory=%q enforce=%v", again.WorkLimits.MemoryAdmission, again.EnforceAdmission)
	}
}

func TestSwapThrashSerializesUnderMemoryAdmissionSwitch(t *testing.T) {
	limits := defaultWorkLimits(10) // capacity 8, build 5, thrash capacity 5
	for _, test := range []struct {
		name            string
		memoryAdmission string
		policyEnabled   bool
		used            int
		allowed         bool
		label           string
	}{
		{"memory switch serializes a second build", MemoryAdmissionEnforce, true, 3, false, SwapThrashDeferredDecision},
		{"memory switch still runs a lone build", MemoryAdmissionEnforce, true, 0, true, SwapThrashAdmittedDecision},
		{"shadow stays report-only", MemoryAdmissionShadow, true, 3, true, ""},
		{"disabled policy stays report-only", MemoryAdmissionEnforce, false, 3, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateLimits := limits
			gateLimits.MemoryAdmission = test.memoryAdmission
			enabled := test.policyEnabled
			gate := &workAdmissionGate{
				limits: gateLimits, class: WorkClassBuild, swapThrashEnforced: false,
				memoryGuardsActive: func() bool { return enabled },
				warningCapacityStatus: func() (WorkStatus, bool) {
					return WorkStatus{Used: test.used, Capacity: gateLimits.Capacity}, true
				},
			}
			decision := gate.observe(swapThrashAdmission(true), false, false)
			if decision.Allowed != test.allowed {
				t.Fatalf("allowed=%v want=%v decision=%+v", decision.Allowed, test.allowed, decision)
			}
			if label := gate.decisionLabel(); label != test.label {
				t.Fatalf("label=%q want=%q", label, test.label)
			}
		})
	}
}

func TestMemoryHeadroomSwitchKeepsFailOpenRules(t *testing.T) {
	limits := testWorkLimits()
	limits.MemoryAdmission = MemoryAdmissionEnforce
	allowed := Admission{Allowed: true, Level: LevelNormal}
	noRoomWith := func(running int) func(Admission) (ShadowMemoryDecision, int, bool) {
		return func(Admission) (ShadowMemoryDecision, int, bool) {
			return ShadowMemoryDecision{Reason: headroomNoRoom, PredictedPeakMB: 3000, HeadroomMB: 100}, running, true
		}
	}
	// Policy enabled, enforce_admission off: the gate holds.
	gate := &workAdmissionGate{limits: limits, memoryHeadroom: noRoomWith(2), memoryGuardsActive: func() bool { return true }}
	if decision, held := gate.memoryHeadroomDecision(allowed, false); !held || decision.Dimension != "memory" {
		t.Fatalf("memory switch did not hold: %+v %v", decision, held)
	}
	// An empty queue never waits on memory.
	gate.memoryHeadroom = noRoomWith(0)
	if _, held := gate.memoryHeadroomDecision(allowed, false); held {
		t.Fatal("empty queue waited on memory")
	}
	// A missing sample or status fails open.
	gate.memoryHeadroom = func(Admission) (ShadowMemoryDecision, int, bool) { return ShadowMemoryDecision{}, 0, false }
	if _, held := gate.memoryHeadroomDecision(allowed, false); held {
		t.Fatal("missing sample held work")
	}
	gate.memoryHeadroom = func(Admission) (ShadowMemoryDecision, int, bool) {
		return ShadowMemoryDecision{Reason: headroomNoSample}, 3, true
	}
	if _, held := gate.memoryHeadroomDecision(allowed, false); held {
		t.Fatal("no-sample decision held work")
	}
}

func TestDoctorSwapThrashWarningSaysWhetherItSerializes(t *testing.T) {
	policy := DefaultPolicy(36 * 1024)
	policy.Enabled, policy.EnforceAdmission = true, false
	in := PressureDoctorInput{
		Now: time.Now(), Policy: policy, HasLatest: true,
		Latest: Snapshot{SwapIOAvailable: true, SwapThrashLatched: true, SwapOutMBPerSecond: 80, SwapThrashCause: "agent"},
	}
	if got := strings.Join(swapThrashDoctorWarnings(in), "\n"); !strings.Contains(got, "report-only") {
		t.Fatalf("observe warning claims serialization: %q", got)
	}
	in.Policy.WorkLimits.MemoryAdmission = MemoryAdmissionEnforce
	if got := strings.Join(swapThrashDoctorWarnings(in), "\n"); !strings.Contains(got, "serialized to swap_thrash_capacity") {
		t.Fatalf("memory switch warning lacks serialization: %q", got)
	}
}
