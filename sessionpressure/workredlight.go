package sessionpressure

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The interactive red lane lets an attended agent chat run light work while
// memory or swap is red.
//
// Why it exists (2026-11-15): with 12-14 GB swap on a 16 GB Mac, memory red
// held every operation for the whole episode. Express work (median peak RSS
// 163 MB) from the chat Nico was typing into waited behind the same gate as a
// background build. Holding a 160 MB test does not relieve swap that agent
// trees and apps created; it only stops the person at the keyboard.
//
// It stays narrow because the guard exists to prevent memory-driven kernel
// panics. Every condition must hold:
//
//   - the operation comes from an attended (interactive) session, or is a
//     commit-delivery Darwin check (--workload delivery-darwin, approved by
//     Nico on 2026-11-15).
//   - the pressure level is red, never critical, and the dimension is memory
//     or swap. Storage and unknown dimensions fail closed.
//   - the class weight is within FastLaneMaxWeight (the express classes).
//   - the class's measured p90 command peak RSS is known and within
//     InteractiveRedLightMaxPeakRSSMB.
//   - all coordinated work, this operation included, stays within
//     InteractiveRedLightCapacity. The lane can never put more than that much
//     leased work on a red host.

// RedLightAdmittedDecision is the telemetry label of an operation the lane
// admitted.
const RedLightAdmittedDecision = "interactive_red_light_admitted"

const (
	defaultInteractiveRedLightCapacity     = 2
	defaultInteractiveRedLightMaxPeakRSSMB = 1024
	redLightMinimumSamples                 = 5
)

type redLightRefusal string

const (
	redLightDisabled           redLightRefusal = "disabled"
	redLightWrongDimension     redLightRefusal = "dimension_not_memory"
	redLightNotRed             redLightRefusal = "level_not_red"
	redLightTooHeavy           redLightRefusal = "weight_above_ceiling"
	redLightCalibrationMissing redLightRefusal = "peak_rss_calibration_unavailable"
	redLightTooLarge           redLightRefusal = "peak_rss_above_ceiling"
	redLightStatusUnknown      redLightRefusal = "coordinator_status_unavailable"
	redLightCapacityFull       redLightRefusal = "red_lane_capacity_full"
)

type redLightInputs struct {
	Capacity      int
	Level         Level
	Dimension     string
	Weight        int
	MaxWeight     int
	PeakRSSMB     float64
	PeakRSSKnown  bool
	MaxPeakRSSMB  int
	StatusKnown   bool
	ProjectedUsed int
}

// evaluateRedLight is the pure predicate. The caller has already established
// that the operation is interactive.
func evaluateRedLight(inputs redLightInputs) (bool, redLightRefusal) {
	if inputs.Capacity <= 0 {
		return false, redLightDisabled
	}
	if inputs.Dimension != "memory" && inputs.Dimension != "swap" {
		return false, redLightWrongDimension
	}
	if inputs.Level != LevelRed {
		return false, redLightNotRed
	}
	if inputs.MaxWeight <= 0 || inputs.Weight > inputs.MaxWeight {
		return false, redLightTooHeavy
	}
	if !inputs.PeakRSSKnown {
		return false, redLightCalibrationMissing
	}
	if inputs.MaxPeakRSSMB <= 0 || inputs.PeakRSSMB > float64(inputs.MaxPeakRSSMB) {
		return false, redLightTooLarge
	}
	if !inputs.StatusKnown {
		return false, redLightStatusUnknown
	}
	if inputs.ProjectedUsed > inputs.Capacity {
		return false, redLightCapacityFull
	}
	return true, ""
}

func resolvedRedLightLimits(limits WorkLimits) (capacity, maxPeakRSSMB int) {
	capacity = limits.InteractiveRedLightCapacity
	if capacity == 0 {
		capacity = min(defaultInteractiveRedLightCapacity, max(1, limits.Capacity))
	}
	maxPeakRSSMB = limits.InteractiveRedLightMaxPeakRSSMB
	if maxPeakRSSMB == 0 {
		maxPeakRSSMB = defaultInteractiveRedLightMaxPeakRSSMB
	}
	return capacity, maxPeakRSSMB
}

// redLightDecision applies the predicate to one live observation. The bool
// reports whether the lane owned the decision; when false the caller keeps
// its normal fail-closed path.
func (gate *workAdmissionGate) redLightDecision(admission Admission, dimension string, ownsLease bool) (workAdmissionDecision, bool) {
	if gate == nil || !gate.interactive || gate.class == "" {
		return workAdmissionDecision{}, false
	}
	limits := normalizeWorkLimits(gate.limits)
	capacity, maxPeakRSSMB := resolvedRedLightLimits(limits)
	weight, err := limits.Weight(gate.class)
	if err != nil {
		return workAdmissionDecision{}, false
	}
	var peak float64
	peakKnown := false
	if gate.classPeakRSSP90MB != nil {
		var peakMB int64
		peakMB, peakKnown = gate.classPeakRSSP90MB()
		peak = float64(peakMB)
	}
	var status WorkStatus
	statusKnown := false
	if gate.warningCapacityStatus != nil {
		status, statusKnown = gate.warningCapacityStatus()
	}
	used := max(0, status.Used)
	projected := used + weight
	if ownsLease {
		// The own lease is already in Used; counting it twice would revoke the
		// admission because acquisition succeeded.
		projected = used
	}
	admitted, refusal := evaluateRedLight(redLightInputs{
		Capacity: capacity, Level: admission.Level, Dimension: dimension,
		Weight: weight, MaxWeight: limits.FastLaneMaxWeight,
		PeakRSSMB: peak, PeakRSSKnown: peakKnown, MaxPeakRSSMB: maxPeakRSSMB,
		StatusKnown: statusKnown, ProjectedUsed: projected,
	})
	if !admitted {
		gate.redLightRefusal = refusal
		return workAdmissionDecision{}, false
	}
	gate.redLightAdmitted = true
	gate.redLightRefusal = ""
	return workAdmissionDecision{
		Allowed: true, Dimension: dimension,
		Reason: fmt.Sprintf("interactive red lane: %s weight %d (<=%d), p90 peak RSS %.0f MB (<=%d), coordinated work %d of %d red-lane units; host: %s",
			gate.class, weight, limits.FastLaneMaxWeight, peak, maxPeakRSSMB, projected, capacity, AdmissionReason(admission)),
	}, true
}

// calibratedClassPeakRSSP90MB is the p90 command peak RSS of one class over
// the calibration window. Too few samples is no evidence, so it fails closed.
func calibratedClassPeakRSSP90MB(dir string, class WorkClass, now func() time.Time) (int64, bool) {
	if strings.TrimSpace(dir) == "" || class == "" {
		return 0, false
	}
	if now == nil {
		now = time.Now
	}
	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{Since: now().Add(-fastLaneCalibrationWindow), Class: class})
	if err != nil {
		return 0, false
	}
	return peakRSSP90MB(events)
}

func peakRSSP90MB(events []WorkEvent) (int64, bool) {
	samples := make([]float64, 0, len(events))
	for _, event := range events {
		if event.Event == WorkEventCompleted && event.CommandPeakRSSMB > 0 {
			samples = append(samples, event.CommandPeakRSSMB)
		}
	}
	if len(samples) < redLightMinimumSamples {
		return 0, false
	}
	sort.Float64s(samples)
	index := (len(samples)*9+9)/10 - 1
	return int64(samples[min(index, len(samples)-1)] + 0.5), true
}

func newCalibratedClassPeakRSSP90(dir string, class WorkClass, now func() time.Time) func() (int64, bool) {
	return newMemoizedCalibration(func() (int64, bool) {
		return calibratedClassPeakRSSP90MB(dir, class, now)
	}, now)
}

// interactiveSessionEnv names the environment that marks an attended agent
// chat. NDEV_INTERACTIVE wins when set (1 or 0); otherwise Claude Code's
// CLAUDE_CODE_SESSION_ATTENDED, which it sets for processes spawned by a
// session a person is attending, decides.
const (
	InteractiveEnv             = "NDEV_INTERACTIVE"
	claudeSessionAttendedEnv   = "CLAUDE_CODE_SESSION_ATTENDED"
	interactiveSourceFlag      = "flag"
	interactiveSourceEnvNDev   = "env:" + InteractiveEnv
	interactiveSourceEnvClaude = "env:" + claudeSessionAttendedEnv
	// interactiveSourceDeliveryDarwin: the commit-delivery worker's Darwin
	// checks, by explicit --workload delivery-darwin.
	interactiveSourceDeliveryDarwin = "workload:" + WorkloadDeliveryDarwin
)

func interactiveFromEnv(getenv func(string) string) (bool, string) {
	if getenv == nil {
		return false, ""
	}
	switch strings.TrimSpace(getenv(InteractiveEnv)) {
	case "1", "true", "yes":
		return true, interactiveSourceEnvNDev
	case "0", "false", "no":
		return false, ""
	}
	switch strings.TrimSpace(getenv(claudeSessionAttendedEnv)) {
	case "1", "true":
		return true, interactiveSourceEnvClaude
	}
	return false, ""
}
