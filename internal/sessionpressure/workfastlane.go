package sessionpressure

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// The admission gate exists to protect the host from sustained aggregate load.
// Holding a 5-second, weight-1 operation while most of the weighted ceiling is
// idle does not serve that purpose: measured over 2026-07-20..25, 11.7% of all
// operations waited more than 30s to perform under 60s of work, and the lightest
// class carried a 50.9s median hold. The fast lane is the narrow exemption for
// exactly that population.
//
// It is deliberately conservative. Every one of these must hold:
//
//   - the pressure dimension is CPU. Memory, swap, and storage fail closed,
//     because the kernel-panic risk this guard was built for is memory-gated.
//   - the class weight is within the configured fast-lane ceiling.
//   - the class's measured p95 runtime is under the configured ceiling, sourced
//     from the same calibration the rest of the system uses.
//   - free weighted capacity can actually fit this operation right now.
//   - the red reading is not dominated by work already under lease. If the
//     coordinator's own admitted work is what is burning the CPU, admitting more
//     of it is exactly wrong; if unmanaged agent trees are burning it, latching
//     only punishes the clients that opted in. Coordinated-work CPU attribution
//     answers this directly when present; when it is absent — the common case at
//     21% availability — a majority-idle weighted ceiling answers it indirectly,
//     because the coordinator cannot be burning CPU it never admitted.

// FastLaneDecisionReason is recorded on the admitting event so the population is
// measurable, auditable, and revocable from telemetry alone.
const FastLaneDecisionReason = "fast_lane_admitted"

// fastLaneRefusal explains why an operation did not qualify. It is returned for
// telemetry only; the caller falls through to the normal latch path.
type fastLaneRefusal string

const (
	fastLaneDisabled            fastLaneRefusal = "disabled"
	fastLaneWrongDimension      fastLaneRefusal = "non_cpu_dimension"
	fastLaneTooHeavy            fastLaneRefusal = "weight_above_ceiling"
	fastLaneTooLong             fastLaneRefusal = "runtime_above_ceiling"
	fastLaneCalibrationMissing  fastLaneRefusal = "calibration_unavailable"
	fastLaneNoCapacity          fastLaneRefusal = "weighted_capacity_unavailable"
	fastLaneCoordinatedDominant fastLaneRefusal = "coordinated_work_dominates_cpu"
)

// fastLaneInputs is the evidence the predicate needs. Keeping it a plain value
// makes the decision a pure function that tests can drive exhaustively without a
// live host, a coordinator, or a calibration store.
type fastLaneInputs struct {
	Enabled                  bool
	Dimension                string
	Weight                   int
	MaxWeight                int
	CalibratedP95RuntimeMS   int64
	CalibrationAvailable     bool
	MaxRuntimeMS             int64
	FreeCapacity             int
	Capacity                 int
	CoordinatedCPUAvailable  bool
	CoordinatedCPUPercent    float64
	CoordinatedCPUCeilingPct float64
}

// evaluateFastLane returns whether the operation qualifies, and if not, why.
func evaluateFastLane(inputs fastLaneInputs) (bool, fastLaneRefusal) {
	if !inputs.Enabled {
		return false, fastLaneDisabled
	}
	if inputs.Dimension != "cpu" {
		return false, fastLaneWrongDimension
	}
	if inputs.MaxWeight <= 0 || inputs.Weight > inputs.MaxWeight {
		return false, fastLaneTooHeavy
	}
	// No measured history means no evidence that this class is short. Fail closed
	// to the latch rather than guessing on the permissive side.
	if !inputs.CalibrationAvailable {
		return false, fastLaneCalibrationMissing
	}
	if inputs.MaxRuntimeMS <= 0 || inputs.CalibratedP95RuntimeMS > inputs.MaxRuntimeMS {
		return false, fastLaneTooLong
	}
	// The weighted ceiling stays authoritative. The fast lane changes when an
	// operation may contend for capacity, never how much capacity exists.
	if inputs.FreeCapacity < inputs.Weight {
		return false, fastLaneNoCapacity
	}
	if inputs.CoordinatedCPUAvailable {
		ceiling := inputs.CoordinatedCPUCeilingPct
		if ceiling <= 0 {
			ceiling = 50
		}
		if inputs.CoordinatedCPUPercent > ceiling {
			return false, fastLaneCoordinatedDominant
		}
		return true, ""
	}
	// Attribution is missing far more often than it is present — measured at 21%
	// availability across CPU-dimension events on this host — so failing closed
	// here would leave the fast lane inert most of the time.
	//
	// Free weighted capacity is independent evidence for the same question. The
	// coordinator can only run what it has admitted, so a host with most of its
	// ceiling idle cannot have coordinated work dominating the CPU; whatever is
	// burning it is unmanaged, and latching this operation would punish the one
	// client that opted into coordination while doing nothing about the cause.
	// Requiring a majority of the ceiling free keeps that inference honest rather
	// than merely permissive.
	if inputs.Capacity > 0 && inputs.FreeCapacity*2 >= inputs.Capacity {
		return true, ""
	}
	return false, fastLaneCoordinatedDominant
}

// fastLaneDecision applies the predicate to one live admission observation. The
// bool reports whether the fast lane owned the decision; when false the caller
// falls through to the unchanged confirm-and-latch path.
func (gate *workAdmissionGate) fastLaneDecision(admission Admission, dimension string) (workAdmissionDecision, bool) {
	if gate == nil || !gate.limits.FastLaneEnabled || gate.class == "" {
		return workAdmissionDecision{}, false
	}
	weight, err := gate.limits.Weight(gate.class)
	if err != nil {
		return workAdmissionDecision{}, false
	}
	calibratedMS, calibrated := gate.calibratedRuntimeMS()
	free, freeKnown := gate.freeCapacity()
	if !freeKnown {
		return workAdmissionDecision{}, false
	}
	available, cpuAvailable, cpuPercent, _, _, _ := coordinatedWorkEvidence(admission)
	admitted, refusal := evaluateFastLane(fastLaneInputs{
		Enabled:                  true,
		Dimension:                dimension,
		Weight:                   weight,
		MaxWeight:                gate.limits.FastLaneMaxWeight,
		CalibratedP95RuntimeMS:   calibratedMS,
		CalibrationAvailable:     calibrated,
		MaxRuntimeMS:             gate.limits.FastLaneMaxRuntimeMS,
		FreeCapacity:             free,
		Capacity:                 gate.limits.Capacity,
		CoordinatedCPUAvailable:  available && cpuAvailable,
		CoordinatedCPUPercent:    cpuPercent,
		CoordinatedCPUCeilingPct: gate.limits.FastLaneCoordinatedCPUCeilingPct,
	})
	if !admitted {
		gate.fastLaneRefusal = refusal
		return workAdmissionDecision{}, false
	}
	gate.fastLaneAdmitted = true
	// State which evidence actually carried the decision. Reporting a coordinated
	// CPU figure that was never measured would be a fabricated justification.
	basis := fmt.Sprintf("coordinated work holds %.1f%% CPU (<=%.1f%%)", cpuPercent, gate.limits.FastLaneCoordinatedCPUCeilingPct)
	if !(available && cpuAvailable) {
		basis = fmt.Sprintf("coordinated-work CPU attribution unavailable; %d of %d weighted capacity idle, so leased work cannot be the cause",
			free, gate.limits.Capacity)
	}
	return workAdmissionDecision{
		Allowed:   true,
		Dimension: "cpu",
		Reason: fmt.Sprintf("fast lane: %s is weight %d (<=%d) with p95 runtime %dms (<=%dms) and %d free capacity; %s",
			gate.class, weight, gate.limits.FastLaneMaxWeight, calibratedMS, gate.limits.FastLaneMaxRuntimeMS, free, basis),
	}, true
}

// decisionLabel is the telemetry marker for how the CPU gate treated this run:
// the fast-lane admission when it applied, otherwise why it did not.
func (gate *workAdmissionGate) decisionLabel() string {
	if gate == nil {
		return ""
	}
	if gate.warningCapacityDeferred {
		return WarningCapacityDeferredDecision
	}
	if gate.warningCapacityAdmitted {
		return WarningCapacityAdmittedDecision
	}
	if gate.fastLaneAdmitted {
		return FastLaneDecisionReason
	}
	if gate.fastLaneRefusal != "" {
		return "fast_lane_refused:" + string(gate.fastLaneRefusal)
	}
	return ""
}

func (gate *workAdmissionGate) calibratedRuntimeMS() (int64, bool) {
	if gate == nil || gate.classRuntimeP95MS == nil {
		return 0, false
	}
	return gate.classRuntimeP95MS()
}

func (gate *workAdmissionGate) freeCapacity() (int, bool) {
	if gate == nil || gate.freeWeightedCapacity == nil {
		return 0, false
	}
	return gate.freeWeightedCapacity()
}

// newCalibratedClassRuntimeP95 returns a memoized accessor for one class's
// measured p95 runtime.
//
// Memoization is not an optimization here, it is a correctness property. Reading
// the corpus costs a full parse of every retained day shard — 24 MB and ~310 ms
// on this host — and the admission gate polls every 2 s per waiting process. Doing
// it per poll would burn more than a core across a handful of waiters at exactly
// the moment the host is already CPU-red, so the guard would deepen the pressure
// it exists to relieve. A class's p95 does not meaningfully move inside one wait,
// so one read per process (refreshed on a slow TTL for long waits) is both cheap
// and accurate.
func newCalibratedClassRuntimeP95(dir string, class WorkClass, now func() time.Time) func() (int64, bool) {
	return newMemoizedCalibration(func() (int64, bool) {
		return calibratedClassRuntimeP95MS(dir, class, now)
	}, now)
}

// newMemoizedCalibration wraps one expensive load behind a TTL. A miss is cached
// exactly like a hit — otherwise a host with no calibration history would re-read
// the whole corpus on every single poll, which is the worst case rather than the
// safe one.
func newMemoizedCalibration(load func() (int64, bool), now func() time.Time) func() (int64, bool) {
	if now == nil {
		now = time.Now
	}
	var (
		mutex     sync.Mutex
		loaded    bool
		loadedAt  time.Time
		runtimeMS int64
		available bool
	)
	return func() (int64, bool) {
		mutex.Lock()
		defer mutex.Unlock()
		if loaded && now().Sub(loadedAt) < fastLaneCalibrationTTL {
			return runtimeMS, available
		}
		runtimeMS, available = load()
		loaded = true
		loadedAt = now()
		return runtimeMS, available
	}
}

// calibratedClassRuntimeP95MS reads the measured p95 runtime for one class from
// the same event corpus every other calibration consumer uses. There is
// deliberately no second runtime estimate in this codebase.
func calibratedClassRuntimeP95MS(dir string, class WorkClass, now func() time.Time) (int64, bool) {
	if strings.TrimSpace(dir) == "" || class == "" {
		return 0, false
	}
	if now == nil {
		now = time.Now
	}
	current := now()
	since := current.Add(-fastLaneCalibrationWindow)
	events, err := NewWorkEventStore(dir).Read(WorkEventFilter{Since: since, Class: class})
	if err != nil || len(events) == 0 {
		return 0, false
	}
	stats := SummarizeWorkEvents(events, since, current)
	for _, classStats := range stats.ByClass {
		if classStats.Class != class {
			continue
		}
		// A handful of samples is not evidence. Require enough completions that
		// the p95 means something before letting it open a gate.
		if classStats.Completed < fastLaneMinimumSamples || classStats.Runtime.P95MS <= 0 {
			return 0, false
		}
		return classStats.Runtime.P95MS, true
	}
	return 0, false
}

// fastLaneCalibrationWindow bounds how far back runtime evidence is trusted, and
// fastLaneMinimumSamples bounds how little evidence may open the gate.
const (
	fastLaneCalibrationWindow = 24 * time.Hour
	fastLaneMinimumSamples    = 5
	// fastLaneCalibrationTTL bounds how long one process reuses its calibration
	// read. Long waits refresh occasionally; short ones read the corpus once.
	fastLaneCalibrationTTL = 5 * time.Minute
)

func coordinatorFreeCapacity(coordinator *WorkCoordinator) (int, bool) {
	if coordinator == nil {
		return 0, false
	}
	statusCtx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	status, err := coordinator.Status(statusCtx)
	if err != nil {
		return 0, false
	}
	return status.Available, true
}
