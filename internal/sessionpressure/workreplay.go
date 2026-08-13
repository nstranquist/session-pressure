package sessionpressure

import (
	"sort"
	"time"
)

type WorkReplayClassMetrics struct {
	Class     WorkClass `json:"class"`
	Count     int       `json:"count"`
	WaitP95MS int64     `json:"wait_p95_ms"`
}

type WorkReplayPolicyMetrics struct {
	Policy                    string                   `json:"policy"`
	Operations                int                      `json:"operations"`
	ContentionDecisions       int                      `json:"contention_decisions"`
	ShortRuntimeLongWait      int                      `json:"short_runtime_long_wait"`
	MaximumBypasses           int                      `json:"maximum_bypasses"`
	CapacityViolations        int                      `json:"capacity_violations"`
	ProtectedBypassViolations int                      `json:"protected_bypass_violations"`
	ByClass                   []WorkReplayClassMetrics `json:"by_class"`
}

type WorkReplayComparison struct {
	SchemaVersion              int                      `json:"schema_version"`
	TraceStart                 time.Time                `json:"trace_start"`
	TraceEnd                   time.Time                `json:"trace_end"`
	FIFO                       WorkReplayPolicyMetrics  `json:"fifo"`
	Candidate                  WorkReplayPolicyMetrics  `json:"candidate"`
	LegacyCalibration          WorkReplayPolicyMetrics  `json:"legacy_calibration"`
	FeasibleFloor              WorkReplayPolicyMetrics  `json:"feasible_floor"`
	MinimumSampleMet           bool                     `json:"minimum_sample_met"`
	RegressionSamplesRequired  int                      `json:"regression_samples_required"`
	RegressionSampleScope      string                   `json:"regression_sample_scope"`
	RegressionClassesEvaluated []WorkClass              `json:"regression_classes_evaluated"`
	RegressionEvaluatedSamples []WorkClassSLOSample     `json:"regression_evaluated_samples"`
	RegressionClassesDeferred  []WorkReplayClassMetrics `json:"regression_classes_deferred"`
	SafetyGatesPass            bool                     `json:"safety_gates_pass"`
	PerformanceGatesPass       bool                     `json:"performance_gates_pass"`
	CalibrationGatesPass       bool                     `json:"calibration_gates_pass"`
	SelectorGatePass           bool                     `json:"selector_gate_pass"`
	SelectorBenchmark          WorkSelectorBenchmark    `json:"selector_benchmark"`
	PromotionReady             bool                     `json:"promotion_ready"`
	ReviewSignals              []string                 `json:"review_signals"`
}

// WorkReplayMinimumRegressionSamples is the smallest per-class population on
// which a p95 regression comparison is meaningful. Smaller samples remain
// visible as deferred evidence; they do not weaken capacity or starvation
// safety gates.
const WorkReplayMinimumRegressionSamples = WorkP95MinimumSamples

type replayJob struct {
	id             string
	class          WorkClass
	weight         int
	queuedAt       time.Time
	runtime        time.Duration
	bypasses       int
	lastBypassedAt *time.Time
	protectedAt    *time.Time
}

type replayRunning struct {
	job      replayJob
	finishAt time.Time
}

// ReplayWorkEvents performs a scheduling-only replay. It deliberately excludes
// host-pressure delay because historical lifecycle rows do not contain every
// pressure sample; both policies receive the same arrivals, weights, and
// observed runtimes, so the comparison isolates queue policy.
func ReplayWorkEvents(events []WorkEvent, limits WorkLimits) WorkReplayComparison {
	limits = normalizeWorkLimits(limits)
	type traceJob struct {
		replayJob
		terminal      bool
		terminalEvent WorkEventType
	}
	byID := map[string]*traceJob{}
	var traceStart, traceEnd time.Time
	for _, event := range events {
		job := byID[event.OperationID]
		if job == nil {
			weight, _ := limits.Weight(event.Class)
			job = &traceJob{replayJob: replayJob{id: event.OperationID, class: event.Class, weight: weight}}
			byID[event.OperationID] = job
		}
		if event.Event == WorkEventQueued && (job.queuedAt.IsZero() || event.Timestamp.Before(job.queuedAt)) {
			job.queuedAt = event.Timestamp.UTC()
		}
		if isWorkTerminalEvent(event.Event) && (!job.terminal || (job.terminalEvent == WorkEventExpired && event.Event != WorkEventExpired)) {
			job.terminal = true
			job.terminalEvent = event.Event
			job.runtime = time.Duration(event.RuntimeMillis) * time.Millisecond
		}
	}
	jobs := make([]replayJob, 0, len(byID))
	for _, job := range byID {
		if !job.terminal || job.terminalEvent == WorkEventExpired || job.runtime <= 0 || job.queuedAt.IsZero() || job.weight < 1 || job.weight > limits.Capacity {
			continue
		}
		jobs = append(jobs, job.replayJob)
		if traceStart.IsZero() || job.queuedAt.Before(traceStart) {
			traceStart = job.queuedAt
		}
		end := job.queuedAt.Add(job.runtime)
		if end.After(traceEnd) {
			traceEnd = end
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].queuedAt.Equal(jobs[j].queuedAt) {
			return jobs[i].id < jobs[j].id
		}
		return jobs[i].queuedAt.Before(jobs[j].queuedAt)
	})
	fifo := replayPolicy(jobs, limits, replayPolicyFIFO)
	candidate := replayPolicy(jobs, limits, replayPolicyCandidate)
	legacyLimits := limits
	legacyLimits.BuildWeight = max(1, (legacyLimits.Capacity*2+2)/3)
	legacyCalibration := replayPolicy(reweightReplayJobs(jobs, legacyLimits), legacyLimits, replayPolicyCandidate)
	legacyCalibration.Policy = WorkSchedulingPolicy + "-legacy-build-weight"
	feasibleFloor := replayPolicy(jobs, limits, replayPolicyFeasibleFloor)
	comparison := WorkReplayComparison{
		SchemaVersion: 4, TraceStart: traceStart, TraceEnd: traceEnd,
		FIFO: fifo, Candidate: candidate, LegacyCalibration: legacyCalibration, FeasibleFloor: feasibleFloor,
		RegressionSamplesRequired: WorkReplayMinimumRegressionSamples, RegressionSampleScope: WorkSlowdownSLOSampleScope,
		RegressionClassesEvaluated: []WorkClass{}, RegressionEvaluatedSamples: []WorkClassSLOSample{},
		RegressionClassesDeferred: []WorkReplayClassMetrics{}, ReviewSignals: []string{},
	}
	comparison.MinimumSampleMet = candidate.Operations >= 100 && candidate.ContentionDecisions >= 20
	comparison.SafetyGatesPass = candidate.CapacityViolations == 0 && candidate.ProtectedBypassViolations == 0 && candidate.MaximumBypasses <= workMaximumBypasses
	fifoByClass := replayClassMap(fifo.ByClass)
	candidateByClass := replayClassMap(candidate.ByClass)
	floorByClass := replayClassMap(feasibleFloor.ByClass)
	performancePass := true
	for _, class := range []WorkClass{WorkClassTest, WorkClassBrowser} {
		baseline := fifoByClass[class].WaitP95MS
		floor := floorByClass[class].WaitP95MS
		// The floor is the best result achievable by oldest-feasible selection
		// without a starvation reservation. Require the deployable policy to close
		// at least 25% of that avoidable gap instead of demanding an impossible
		// 50% reduction in capacity-bound wait.
		if !closesReplayGap(baseline, candidateByClass[class].WaitP95MS, floor, 25) {
			performancePass = false
		}
	}
	for _, class := range []WorkClass{WorkClassTest, WorkClassBuild, WorkClassBrowser, WorkClassEmulator, WorkClassHeavy, WorkClassBenchmark} {
		candidateClass := candidateByClass[class]
		if candidateClass.Count == 0 {
			continue
		}
		if candidateClass.Count < WorkReplayMinimumRegressionSamples {
			comparison.RegressionClassesDeferred = append(comparison.RegressionClassesDeferred, candidateClass)
			continue
		}
		comparison.RegressionClassesEvaluated = append(comparison.RegressionClassesEvaluated, class)
		comparison.RegressionEvaluatedSamples = append(comparison.RegressionEvaluatedSamples, WorkClassSLOSample{Class: class, Samples: candidateClass.Count})
		baseline := fifoByClass[class].WaitP95MS
		if baseline > 0 && candidateClass.WaitP95MS*100 > baseline*120 {
			performancePass = false
		}
	}
	if !closesReplayGap(int64(fifo.ShortRuntimeLongWait), int64(candidate.ShortRuntimeLongWait), int64(feasibleFloor.ShortRuntimeLongWait), 50) {
		performancePass = false
	}
	comparison.PerformanceGatesPass = performancePass
	legacyBuild := replayClassMap(legacyCalibration.ByClass)[WorkClassBuild]
	candidateBuild := candidateByClass[WorkClassBuild]
	comparison.CalibrationGatesPass = legacyBuild.Count == 0 || (candidateBuild.WaitP95MS <= legacyBuild.WaitP95MS && candidate.ShortRuntimeLongWait <= legacyCalibration.ShortRuntimeLongWait)
	comparison.PromotionReady = false
	if !comparison.MinimumSampleMet {
		comparison.ReviewSignals = append(comparison.ReviewSignals, "replay needs at least 100 closed operations and 20 contention decisions")
	}
	if !comparison.SafetyGatesPass {
		comparison.ReviewSignals = append(comparison.ReviewSignals, "candidate replay violated a capacity or starvation invariant")
	}
	if !comparison.PerformanceGatesPass {
		comparison.ReviewSignals = append(comparison.ReviewSignals, "candidate replay did not meet wait, regression, short-work, or selector-overhead gates")
	}
	if !comparison.CalibrationGatesPass {
		comparison.ReviewSignals = append(comparison.ReviewSignals, "build-weight calibration regressed build p95 wait or short-runtime long-wait operations")
	}
	if len(comparison.RegressionClassesDeferred) > 0 {
		comparison.ReviewSignals = append(comparison.ReviewSignals, "per-class p95 regression gate deferred for classes with fewer than 20 terminal-runtime samples")
	}
	return comparison
}

func reweightReplayJobs(jobs []replayJob, limits WorkLimits) []replayJob {
	weighted := append([]replayJob(nil), jobs...)
	for index := range weighted {
		weight, err := limits.Weight(weighted[index].class)
		if err == nil {
			weighted[index].weight = weight
		}
	}
	return weighted
}

// ApplySelectorBenchmark combines a non-deterministic wall-clock benchmark with
// the deterministic replay. Keeping the two phases separate makes replay byte-
// stable while still requiring measured lock-path overhead before promotion.
func (comparison *WorkReplayComparison) ApplySelectorBenchmark(benchmark WorkSelectorBenchmark) {
	if comparison == nil {
		return
	}
	comparison.SelectorBenchmark = benchmark
	comparison.SelectorGatePass = benchmark.Pass
	comparison.PromotionReady = comparison.MinimumSampleMet && comparison.SafetyGatesPass && comparison.PerformanceGatesPass && comparison.CalibrationGatesPass && comparison.SelectorGatePass
	if !benchmark.Pass {
		comparison.ReviewSignals = append(comparison.ReviewSignals, "selector overhead benchmark exceeded its p95 budget")
	}
}

func closesReplayGap(baseline, candidate, floor int64, requiredPercent int64) bool {
	avoidable := baseline - floor
	if baseline <= 0 || avoidable <= 0 {
		return candidate <= baseline
	}
	closed := baseline - candidate
	return closed > 0 && closed*100 >= avoidable*requiredPercent
}

func replayClassMap(rows []WorkReplayClassMetrics) map[WorkClass]WorkReplayClassMetrics {
	result := make(map[WorkClass]WorkReplayClassMetrics, len(rows))
	for _, row := range rows {
		result[row.Class] = row
	}
	return result
}

type replayPolicyMode int

const (
	replayPolicyFIFO replayPolicyMode = iota
	replayPolicyCandidate
	replayPolicyFeasibleFloor
)

func replayPolicy(jobs []replayJob, limits WorkLimits, mode replayPolicyMode) WorkReplayPolicyMetrics {
	policy := WorkSchedulingPolicyFIFO
	if mode == replayPolicyCandidate {
		policy = WorkSchedulingPolicy
	} else if mode == replayPolicyFeasibleFloor {
		policy = "oldest-feasible-floor"
	}
	result := WorkReplayPolicyMetrics{Policy: policy, Operations: len(jobs)}
	if len(jobs) == 0 {
		return result
	}
	queue := []replayJob{}
	running := []replayRunning{}
	waits := map[WorkClass][]int64{}
	next := 0
	now := jobs[0].queuedAt
	for next < len(jobs) || len(queue) > 0 || len(running) > 0 {
		keptRunning := running[:0]
		used := 0
		for _, active := range running {
			if active.finishAt.After(now) {
				keptRunning = append(keptRunning, active)
				used += active.job.weight
			}
		}
		running = keptRunning
		for next < len(jobs) && !jobs[next].queuedAt.After(now) {
			queue = append(queue, jobs[next])
			next++
		}
		for len(queue) > 0 {
			selected := -1
			if mode == replayPolicyCandidate {
				waiters := make([]WorkWaiterRecord, len(queue))
				for index, job := range queue {
					// Keep the class in the replay record. The live selector uses
					// it for express ride-through and any future class-specific
					// admission rule; dropping it would make replay less faithful
					// while still producing superficially valid wait metrics.
					waiters[index] = WorkWaiterRecord{
						OperationID:    job.id,
						Class:          job.class,
						Weight:         job.weight,
						QueuedAt:       job.queuedAt,
						BypassCount:    job.bypasses,
						LastBypassedAt: job.lastBypassedAt,
						ProtectedAt:    job.protectedAt,
					}
				}
				decision := selectWorkWaiter(waiters, used, limits.Capacity, now, workGreenExpressWindow{}, 0)
				for index := range queue {
					if queue[index].id == decision.SelectedOperationID {
						selected = index
						break
					}
				}
				if selected > 0 {
					result.ContentionDecisions++
					for _, index := range decision.BypassedIndexes {
						if protected, _ := workWaiterProtection(waiters[index], limits.Capacity, now); protected {
							result.ProtectedBypassViolations++
						}
						queue[index].bypasses++
						if queue[index].lastBypassedAt == nil {
							bypassedAt := now
							queue[index].lastBypassedAt = &bypassedAt
						}
						if queue[index].bypasses >= workMaximumBypasses {
							protectedAt := now
							queue[index].protectedAt = &protectedAt
						}
						result.MaximumBypasses = max(result.MaximumBypasses, queue[index].bypasses)
					}
				}
			} else if mode == replayPolicyFeasibleFloor {
				for index, job := range queue[:min(len(queue), workSelectorScanLimit)] {
					if job.weight+used <= limits.Capacity {
						selected = index
						break
					}
				}
				if selected > 0 {
					result.ContentionDecisions++
				}
			} else if queue[0].weight+used <= limits.Capacity {
				selected = 0
			}
			if selected < 0 {
				if len(queue) > 0 && len(running) > 0 {
					result.ContentionDecisions++
				}
				break
			}
			job := queue[selected]
			if used+job.weight > limits.Capacity {
				result.CapacityViolations++
				break
			}
			waitMS := max(int64(0), now.Sub(job.queuedAt).Milliseconds())
			waits[job.class] = append(waits[job.class], waitMS)
			if waitMS >= int64(time.Minute/time.Millisecond) && job.runtime <= 10*time.Second {
				result.ShortRuntimeLongWait++
			}
			used += job.weight
			running = append(running, replayRunning{job: job, finishAt: now.Add(job.runtime)})
			queue = append(queue[:selected], queue[selected+1:]...)
		}
		if next >= len(jobs) && len(running) == 0 {
			break
		}
		var advance time.Time
		if next < len(jobs) {
			advance = jobs[next].queuedAt
		}
		for _, active := range running {
			if advance.IsZero() || active.finishAt.Before(advance) {
				advance = active.finishAt
			}
		}
		if !advance.After(now) {
			advance = now.Add(time.Millisecond)
		}
		now = advance
	}
	for _, class := range []WorkClass{WorkClassTest, WorkClassBuild, WorkClassBrowser, WorkClassEmulator, WorkClassHeavy, WorkClassBenchmark} {
		stats := durationStats(waits[class])
		result.ByClass = append(result.ByClass, WorkReplayClassMetrics{Class: class, Count: len(waits[class]), WaitP95MS: stats.P95MS})
	}
	return result
}
