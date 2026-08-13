package sessionpressure

import (
	"fmt"
)

const (
	WarningCapacityAdmittedDecision = "warning_capacity_admitted"
	WarningCapacityDeferredDecision = "warning_capacity_deferred"
)

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
