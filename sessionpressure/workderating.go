package sessionpressure

import (
	"fmt"
)

const (
	WarningCapacityAdmittedDecision = "warning_capacity_admitted"
	WarningCapacityDeferredDecision = "warning_capacity_deferred"
	SwapThrashAdmittedDecision      = "swap_thrash_admitted"
	SwapThrashDeferredDecision      = "swap_thrash_deferred"
)

// swapThrashDecision serializes new heavy work while the resident's swap
// thrash latch is set under an enforcing profile. It can only defer: work that
// fits falls through to the CPU, memory, and storage gates unchanged, so the
// derate never weakens another safety floor. A lone operation is always
// admitted, so a thrash caused outside the queue (browser, editor) can slow
// agents but never starve them.
func (gate *workAdmissionGate) swapThrashDecision(admission Admission, ownsLease bool) (workAdmissionDecision, bool) {
	if gate == nil || !gate.swapThrashEnforced || gate.class == "" || !admission.Allowed ||
		admission.Snapshot == nil || !admission.Snapshot.SwapIOAvailable || !admission.Snapshot.SwapThrashLatched ||
		gate.warningCapacityStatus == nil {
		return workAdmissionDecision{}, false
	}
	limits := normalizeWorkLimits(gate.limits)
	weight, err := limits.Weight(gate.class)
	if err != nil {
		return workAdmissionDecision{}, false
	}
	status, known := gate.warningCapacityStatus()
	if !known {
		// Observability failure must not become an availability failure.
		return workAdmissionDecision{}, false
	}
	used := max(0, status.Used)
	projectedUsed, alone := used+weight, used == 0
	if ownsLease {
		// Coordinator status already counts this operation after acquisition.
		projectedUsed, alone = used, used <= weight
	}
	reason := fmt.Sprintf(
		"swap thrash (%.1f MB/s swap-out) serializes new heavy work: class=%s weight=%d used=%d projected_used=%d thrash_capacity=%d",
		admission.Snapshot.SwapOutMBPerSecond, gate.class, weight, used, projectedUsed, limits.SwapThrashCapacity,
	)
	if projectedUsed <= limits.SwapThrashCapacity || alone {
		gate.swapThrashAdmitted = true
		return workAdmissionDecision{}, false
	}
	gate.swapThrashDeferred = true
	return workAdmissionDecision{Allowed: false, Dimension: "swap", Reason: reason, RetryInterval: WorkAdmissionRetryInterval}, true
}

// warningPressureDimension recognizes typed host warning evidence. Storage
// pressure retains its independent class-aware admission policy: reducing CPU
// capacity cannot create disk space and would obstruct its express/reclaim
// escape paths.
func warningPressureDimension(admission Admission) string {
	if admission.Level != LevelWarning || admission.Snapshot == nil {
		return ""
	}
	switch admission.Dimension {
	case "cpu", "memory":
		return admission.Dimension
	default:
		return ""
	}
}

// warningCapacityDecision applies a lower effective ceiling to new work while
// leaving the coordinator and all active leases untouched.
func (gate *workAdmissionGate) warningCapacityDecision(admission Admission, ownsLease bool) (workAdmissionDecision, bool) {
	if gate == nil || gate.class == "" {
		return workAdmissionDecision{}, false
	}
	limits := normalizeWorkLimits(gate.limits)
	if !limits.WarningCapacityEnabled {
		// Balanced, Throughput, and Observe intentionally keep warning pressure
		// advisory. Only Interactive opts into a lower arrival ceiling.
		return workAdmissionDecision{}, false
	}
	dimension := warningPressureDimension(admission)
	if dimension == "" {
		return workAdmissionDecision{}, false
	}
	weight, err := limits.Weight(gate.class)
	if err != nil {
		return workAdmissionDecision{}, false
	}
	if gate.latched || gate.warningCapacityStatus == nil {
		return workAdmissionDecision{}, false
	}
	status, known := gate.warningCapacityStatus()
	if !known {
		// Derating must not turn an observability failure into a host-wide
		// availability failure. The normal weighted coordinator remains active.
		return workAdmissionDecision{}, false
	}
	if status.AdmissionLatch != nil && status.AdmissionLatch.Latched {
		// Warning must never weaken a red latch. Fall through to the existing
		// hysteresis path; after recovery, Observe calls the derater again.
		return workAdmissionDecision{}, false
	}
	used := max(0, status.Used)
	projectedUsed := used + weight
	if ownsLease {
		// Coordinator status already includes this operation after acquisition.
		// Counting its weight twice would park work that fits the warning ceiling.
		projectedUsed = used
	}
	reason := fmt.Sprintf(
		"%s warning derates new-work capacity: class=%s weight=%d used=%d projected_used=%d effective_capacity=%d configured_capacity=%d",
		dimension, gate.class, weight, used, projectedUsed, limits.WarningCapacity, limits.Capacity,
	)
	if projectedUsed <= limits.WarningCapacity {
		gate.warningCapacityAdmitted = true
		return workAdmissionDecision{Allowed: true, Dimension: dimension, Reason: reason}, true
	}
	gate.warningCapacityDeferred = true
	return workAdmissionDecision{
		Allowed: false, Dimension: dimension, Reason: reason,
		RetryInterval: WorkAdmissionRetryInterval,
	}, true
}

func warningEffectiveCapacity(admission Admission, limits WorkLimits) int {
	limits = normalizeWorkLimits(limits)
	if limits.WarningCapacityEnabled && warningPressureDimension(admission) != "" {
		return limits.WarningCapacity
	}
	return limits.Capacity
}
