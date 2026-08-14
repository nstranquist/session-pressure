package sessionpressure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type WorkEvaluationScenario struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type WorkEvaluationThresholds struct {
	MaximumMonitorBytesDay int64 `json:"maximum_monitor_bytes_per_day"`
	TargetMonitorBytesDay  int64 `json:"target_monitor_bytes_per_day"`
	MaximumWorkEventBytes  int   `json:"maximum_work_event_bytes"`
	MaximumEvaluationMS    int64 `json:"maximum_evaluation_ms"`
	MaximumSelectorP95US   int64 `json:"maximum_selector_p95_us"`
}

type WorkSelectorBenchmark struct {
	Iterations         int   `json:"iterations"`
	QueueDepth         int   `json:"queue_depth"`
	P95Microseconds    int64 `json:"p95_us"`
	MaxMicroseconds    int64 `json:"max_us"`
	BudgetMicroseconds int64 `json:"budget_us"`
	Pass               bool  `json:"pass"`
}

type WorkEvaluationReport struct {
	SchemaVersion              int                      `json:"schema_version"`
	OK                         bool                     `json:"ok"`
	ScenarioCount              int                      `json:"scenario_count"`
	Passed                     int                      `json:"passed"`
	Failed                     int                      `json:"failed"`
	Scenarios                  []WorkEvaluationScenario `json:"scenarios"`
	ClassWeights               []WorkClassWeight        `json:"class_weights"`
	ProjectedMonitorBytesDay   int64                    `json:"projected_monitor_bytes_per_day"`
	ProjectedTelemetryBytesDay int64                    `json:"projected_telemetry_bytes_per_day"`
	MaximumWorkEventBytes      int                      `json:"maximum_work_event_bytes"`
	RuntimeMilliseconds        int64                    `json:"runtime_ms"`
	ReviewSignals              []string                 `json:"false_positive_review_signals"`
	Thresholds                 WorkEvaluationThresholds `json:"thresholds"`
	DeterministicDigest        string                   `json:"deterministic_digest"`
	SelectorBenchmark          WorkSelectorBenchmark    `json:"selector_benchmark"`
	Replay                     *WorkReplayComparison    `json:"replay,omitempty"`
}

type WorkClassWeight struct {
	Class  WorkClass `json:"class"`
	Weight int       `json:"weight"`
}

func EvaluateWorkSystem(policy Policy) WorkEvaluationReport {
	started := time.Now()
	thresholds := WorkEvaluationThresholds{
		MaximumMonitorBytesDay: policy.ResourceBudgets.MaxTelemetryBytesDay,
		TargetMonitorBytesDay:  policy.ResourceBudgets.MaxTelemetryBytesDay / 2,
		MaximumWorkEventBytes:  1024,
		MaximumEvaluationMS:    250,
		MaximumSelectorP95US:   2000,
	}
	report := WorkEvaluationReport{SchemaVersion: 1, Thresholds: thresholds, ReviewSignals: []string{}}
	add := func(id string, passed bool, detail string) {
		report.Scenarios = append(report.Scenarios, WorkEvaluationScenario{ID: id, Passed: passed, Detail: detail})
	}

	limits := normalizeWorkLimits(policy.WorkLimits)
	for _, class := range AllWorkClasses() {
		weight, _ := limits.Weight(class)
		report.ClassWeights = append(report.ClassWeights, WorkClassWeight{Class: class, Weight: weight})
	}
	add("calibrated-classes", limits.TestWeight < limits.BuildWeight && limits.BrowserWeight <= limits.TestWeight && limits.BuildWeight+limits.TestWeight <= limits.Capacity && 2*limits.BuildWeight > limits.Capacity &&
		limits.ExpressTestWeight < limits.TestWeight && limits.ExpressBuildWeight < limits.BuildWeight &&
		limits.BenchmarkWeight+limits.ExpressTestWeight <= limits.Capacity && limits.BenchmarkWeight < limits.Capacity,
		fmt.Sprintf("capacity=%d express_test=%d test=%d express_build=%d build=%d browser=%d benchmark=%d exclusive=%d",
			limits.Capacity, limits.ExpressTestWeight, limits.TestWeight, limits.ExpressBuildWeight, limits.BuildWeight, limits.BrowserWeight, limits.BenchmarkWeight, limits.Capacity))

	firstID := "00000000000000000000000000000001"
	secondID := "00000000000000000000000000000002"
	queuedAt := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	expressClass, expressOK := ClassifyGoWorkArgv([]string{"go", "test", "./internal/sessionpressure"})
	fullClass, fullOK := ClassifyGoWorkArgv([]string{"go", "test", "./..."})
	expressBuild, expressBuildOK := ClassifyGoWorkArgv([]string{"go", "build", "./cmd/ndev-go"})
	fullBuild, fullBuildOK := ClassifyGoWorkArgv([]string{"go", "build", "./..."})
	add("express-scope-classification", expressOK && fullOK && expressBuildOK && fullBuildOK &&
		expressClass == WorkClassExpressTest && fullClass == WorkClassTest &&
		expressBuild == WorkClassExpressBuild && fullBuild == WorkClassBuild,
		fmt.Sprintf("package test=%s suite test=%s package build=%s suite build=%s", expressClass, fullClass, expressBuild, fullBuild))

	expressWeight, _ := limits.Weight(WorkClassExpressTest)
	benchmarkWeight, _ := limits.Weight(WorkClassBenchmark)
	exclusiveWeight, _ := limits.Weight(WorkClassBenchmarkExclusive)
	add("express-residual-beside-benchmark", ExpressFitsBeside(benchmarkWeight, expressWeight, limits.Capacity) && exclusiveWeight == limits.Capacity && benchmarkWeight < limits.Capacity,
		fmt.Sprintf("benchmark=%d express=%d capacity=%d exclusive=%d", benchmarkWeight, expressWeight, limits.Capacity, exclusiveWeight))

	// When a non-exclusive benchmark holds residual-leaving capacity and a full
	// build head cannot fit, bounded lookahead must still select express work.
	expressLookahead := selectWorkWaiter([]WorkWaiterRecord{
		{OperationID: firstID, Class: WorkClassBuild, Weight: limits.BuildWeight, QueuedAt: queuedAt},
		{OperationID: secondID, Class: WorkClassExpressTest, Weight: expressWeight, QueuedAt: queuedAt.Add(time.Second)},
	}, benchmarkWeight, limits.Capacity, queuedAt.Add(2*time.Second), workGreenExpressWindow{}, 0)
	add("express-lookahead-under-benchmark", expressLookahead.SelectedOperationID == secondID && len(expressLookahead.BypassedIndexes) == 1,
		fmt.Sprintf("selected=%s reason=%s used=%d", expressLookahead.SelectedOperationID, expressLookahead.DecisionReason, benchmarkWeight))

	state := workState{SchemaVersion: workStateSchemaVersion, Waiters: []WorkWaiterRecord{
		{OperationID: secondID, Class: WorkClassBrowser, Weight: limits.BrowserWeight, PID: 2, OwnerIdentity: "owner", QueuedAt: queuedAt, HeartbeatAt: queuedAt},
		{OperationID: firstID, Class: WorkClassBuild, Weight: limits.BuildWeight, PID: 1, OwnerIdentity: "owner", QueuedAt: queuedAt, HeartbeatAt: queuedAt},
	}}
	coordinator := NewWorkCoordinator("/deterministic/evaluation", limits)
	coordinator.ProcessAlive = func(int) bool { return true }
	coordinator.ProcessIdentity = func(int) (string, error) { return "owner", nil }
	_, _, _, reconcileErr := coordinator.reconcile(&state)
	fifoPassed := reconcileErr == nil && len(state.Waiters) == 2 && state.Waiters[0].OperationID == firstID && state.Waiters[1].OperationID == secondID
	add("queue-stable-order", fifoPassed, "equal timestamps order by stable operation identity")

	lookahead := selectWorkWaiter([]WorkWaiterRecord{
		{OperationID: firstID, Weight: limits.BuildWeight, QueuedAt: queuedAt},
		{OperationID: secondID, Weight: limits.BrowserWeight, QueuedAt: queuedAt.Add(time.Second)},
	}, limits.Capacity-limits.TestWeight, limits.Capacity, queuedAt.Add(2*time.Second), workGreenExpressWindow{}, 0)
	add("oldest-feasible-lookahead", lookahead.SelectedOperationID == secondID && len(lookahead.BypassedIndexes) == 1,
		fmt.Sprintf("selected=%s bypassed=%d", lookahead.SelectedOperationID, len(lookahead.BypassedIndexes)))

	protectedByCount := selectWorkWaiter([]WorkWaiterRecord{
		{OperationID: firstID, Weight: limits.BuildWeight, QueuedAt: queuedAt, BypassCount: workMaximumBypasses},
		{OperationID: secondID, Weight: limits.BrowserWeight, QueuedAt: queuedAt.Add(time.Second)},
	}, limits.Capacity-limits.TestWeight, limits.Capacity, queuedAt.Add(2*time.Second), workGreenExpressWindow{}, 0)
	add("bypass-bound-drain", protectedByCount.SelectedOperationID == "" && protectedByCount.ProtectedOperationID == firstID && protectedByCount.DecisionReason == "protected_bounded_drain",
		fmt.Sprintf("selected=%s protected=%s reason=%s", protectedByCount.SelectedOperationID, protectedByCount.ProtectedOperationID, protectedByCount.DecisionReason))

	firstBypassedAt := queuedAt
	protectedByAge := selectWorkWaiter([]WorkWaiterRecord{
		{OperationID: firstID, Weight: limits.BuildWeight, QueuedAt: queuedAt, BypassCount: 1, LastBypassedAt: &firstBypassedAt},
		{OperationID: secondID, Weight: limits.BrowserWeight, QueuedAt: queuedAt.Add(time.Second)},
	}, limits.Capacity-limits.TestWeight, limits.Capacity, queuedAt.Add(WorkReservationAge), workGreenExpressWindow{}, 0)
	add("age-bound-drain", protectedByAge.SelectedOperationID == "" && protectedByAge.ProtectedOperationID == firstID && protectedByAge.DecisionReason == "protected_bounded_drain",
		fmt.Sprintf("selected=%s protected=%s reason=%s", protectedByAge.SelectedOperationID, protectedByAge.ProtectedOperationID, protectedByAge.DecisionReason))

	exclusiveDrain := selectWorkWaiter([]WorkWaiterRecord{
		{OperationID: firstID, Class: WorkClassBenchmarkExclusive, Weight: limits.Capacity, QueuedAt: queuedAt},
		{OperationID: secondID, Class: WorkClassBrowser, Weight: limits.BrowserWeight, QueuedAt: queuedAt.Add(time.Second)},
	}, limits.TestWeight, limits.Capacity, queuedAt, workGreenExpressWindow{}, 0)
	add("exclusive-drain-reservation", exclusiveDrain.SelectedOperationID == "" && exclusiveDrain.ProtectedOperationID == firstID && exclusiveDrain.DecisionReason == "protected_exclusive_drain",
		fmt.Sprintf("selected=%s protected=%s reason=%s", exclusiveDrain.SelectedOperationID, exclusiveDrain.ProtectedOperationID, exclusiveDrain.DecisionReason))

	overrideWaiters := []WorkWaiterRecord{
		{OperationID: firstID, Weight: limits.BrowserWeight, QueuedAt: queuedAt},
		{OperationID: secondID, Weight: limits.TestWeight, QueuedAt: queuedAt.Add(time.Second)},
	}
	overrideDrain, overrideFound := selectOverriddenWorkWaiter(overrideWaiters, limits.Capacity-limits.BrowserWeight, limits.Capacity, secondID)
	overrideSelected, selectedFound := selectOverriddenWorkWaiter(overrideWaiters, 0, limits.Capacity, secondID)
	add("operator-override-safety", overrideFound && selectedFound && overrideDrain.SelectedOperationID == "" && overrideDrain.ProtectedOperationID == secondID && overrideDrain.DecisionReason == "priority_override_bounded_drain" && overrideSelected.SelectedOperationID == secondID,
		fmt.Sprintf("drain=%s protected=%s selected_after_release=%s", overrideDrain.DecisionReason, overrideDrain.ProtectedOperationID, overrideSelected.SelectedOperationID))

	oversizedQueue := make([]WorkWaiterRecord, workSelectorScanLimit+1)
	for index := range oversizedQueue {
		oversizedQueue[index] = WorkWaiterRecord{OperationID: fmt.Sprintf("%032x", index+1), Weight: limits.Capacity + 1, QueuedAt: queuedAt}
	}
	oversizedQueue[len(oversizedQueue)-1].Weight = 1
	boundedScan := selectWorkWaiter(oversizedQueue, 0, limits.Capacity, queuedAt.Add(time.Second), workGreenExpressWindow{}, 0)
	add("selector-scan-bound", boundedScan.SelectedOperationID == "", fmt.Sprintf("scanned_at_most=%d queue=%d", workSelectorScanLimit, len(oversizedQueue)))

	cpuRed := Admission{Allowed: false, Level: LevelRed, Dimension: "cpu", Reasons: []string{"host CPU 99.0% >= red 95.0%"}, Snapshot: &Snapshot{HostCPUAvailable: true, HostCPUPercent: 99}}
	cpuNormal := Admission{Allowed: true, Level: LevelNormal, Snapshot: &Snapshot{HostCPUAvailable: true, HostCPUPercent: 70}}
	transientGate := workAdmissionGate{limits: limits}
	transientSpike := Admission{Allowed: true, Level: LevelRed, Dimension: "cpu", Reasons: []string{"host CPU 99.0% >= red 95.0%"}, Snapshot: &Snapshot{HostCPUAvailable: true, HostCPUPercent: 99}}
	transientFirst := transientGate.Observe(transientSpike, false)
	transientSecond := transientGate.Observe(cpuNormal, false)
	add("cpu-transient-filter", transientFirst.Allowed && transientSecond.Allowed && !transientGate.latched, "one uncorroborated CPU-only red sample remains non-blocking")

	sustainedGate := workAdmissionGate{limits: limits}
	enterOne := sustainedGate.Observe(cpuRed, false)
	enterTwo := sustainedGate.Observe(cpuRed, false)
	cpuWarning := Admission{Allowed: true, Level: LevelWarning, Snapshot: &Snapshot{HostCPUAvailable: true, HostCPUPercent: 82}}
	stillLatched := sustainedGate.Observe(cpuWarning, false)
	releaseOne := sustainedGate.Observe(cpuNormal, false)
	releaseTwo := sustainedGate.Observe(cpuNormal, false)
	add("cpu-hysteresis", !enterOne.Allowed && !enterTwo.Allowed && sustainedGate.cpuRed == 0 && !stillLatched.Allowed && !releaseOne.Allowed && releaseTwo.Allowed && !sustainedGate.latched,
		fmt.Sprintf("enter=%d release=%d threshold=%.1f%%", limits.CPUBlockSamples, limits.CPUReleaseSamples, limits.CPUReleasePercent))

	memoryRed := Admission{Allowed: false, Level: LevelRed, Reasons: []string{"host free memory 10% <= red 15%"}, Snapshot: &Snapshot{HostCPUAvailable: true, HostCPUPercent: 20}}
	memoryDecision := (&workAdmissionGate{limits: limits}).Observe(memoryRed, false)
	add("memory-immediate-block", !memoryDecision.Allowed && memoryDecision.Dimension == "memory", "memory red bypasses CPU confirmation")

	exitZero := 0
	events := []WorkEvent{
		{OperationID: firstID, Class: WorkClassTest, Event: WorkEventQueued},
		{OperationID: firstID, Class: WorkClassTest, Event: WorkEventAcquired, WaitMilliseconds: 120},
		{OperationID: firstID, Class: WorkClassTest, Event: WorkEventStarted},
		{OperationID: firstID, Class: WorkClassTest, Event: WorkEventCompleted, RuntimeMillis: 800, ExitCode: &exitZero},
	}
	stats := SummarizeWorkEvents(events, queuedAt, queuedAt.Add(time.Second))
	add("lifecycle-stats", stats.OperationCount == 1 && stats.OpenOperations == 0 && stats.ByClass[0].Completed == 1 && stats.ByClass[0].Wait.P95MS == 120, "complete lifecycle produces one closed test operation")
	cancelledEvents := append([]WorkEvent(nil), events[:3]...)
	cancelledEvents = append(cancelledEvents, WorkEvent{OperationID: firstID, Class: WorkClassTest, Event: WorkEventCancelled, RuntimeMillis: 400})
	cancelledStats := SummarizeWorkEvents(cancelledEvents, queuedAt, queuedAt.Add(time.Second))
	add("cancellation-accounting", cancelledStats.OpenOperations == 0 && cancelledStats.ByClass[0].Cancelled == 1 && cancelledStats.ReviewSignals.CancelledOperations == 1,
		"cancellation produces one closed terminal outcome and review signal")
	add("lifecycle-stable-order", workEventOrder(WorkEventQueued) < workEventOrder(WorkEventAcquired) && workEventOrder(WorkEventAcquired) < workEventOrder(WorkEventStarted) && workEventOrder(WorkEventStarted) < workEventOrder(WorkEventCompleted),
		"equal-timestamp rows retain queued, acquired, started, terminal order")

	maxEvent := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion, Timestamp: queuedAt, Event: WorkEventCancelled,
		OperationID: firstID, LeaseID: secondID, Class: WorkClassExpressBuild, RequestedClass: WorkClassBuild, Weight: limits.ExpressBuildWeight, PID: 99999,
		SessionDigest: "sha256:" + strings.Repeat("a", 64), CommandDigest: CommandShapeDigest("/usr/bin/example", 99),
		Blocker: WorkBlockerPressure, QueuePosition: 99, QueueDepth: 100, Capacity: limits.Capacity, Used: limits.Capacity,
		WaitMilliseconds: int64((10 * time.Minute) / time.Millisecond), RuntimeMillis: int64((24 * time.Hour) / time.Millisecond),
		PressureLevel: LevelCritical, PressureDimension: "memory", PressureReason: strings.Repeat("p", actionReasonLimit),
		ExitCode: &exitZero, Outcome: strings.Repeat("o", 64),
	}
	maxEvent.EventID = workEventID(maxEvent.OperationID, maxEvent.Event, maxEvent.LeaseID)
	maxEventBody, _ := json.Marshal(maxEvent)
	report.MaximumWorkEventBytes = len(maxEventBody) + 1
	add("work-event-wire-budget", report.MaximumWorkEventBytes <= thresholds.MaximumWorkEventBytes, fmt.Sprintf("maximum=%dB budget=%dB", report.MaximumWorkEventBytes, thresholds.MaximumWorkEventBytes))

	snapshot := Snapshot{
		SchemaVersion: SchemaVersion, Timestamp: queuedAt, Level: LevelNormal, FreePercent: 40, SwapUsedMB: 7000,
		HostCPUPercent: 60, AgentTreeCount: 8, AgentRSSSumMB: 2200, GuardRSSMB: 8,
		GuardIdleCPUDutyPercent: 0.03, SampleDurationP95MS: 700, GuardBudgetOK: true,
		TopAgentTrees: []AgentTree{{Agent: "codex", RootPID: 1, Executable: "codex", ProcessCount: 10, RSSSumMB: 500}},
	}
	heartbeat := TelemetryEvent{SchemaVersion: SchemaVersion, Timestamp: queuedAt, Event: "heartbeat"}
	summary := compactTelemetrySummary(snapshot)
	heartbeat.Summary = &summary
	transition := TelemetryEvent{SchemaVersion: SchemaVersion, Timestamp: queuedAt, Event: "state_transition", Snapshot: &snapshot}
	heartbeatBody, _ := json.Marshal(heartbeat)
	transitionBody, _ := json.Marshal(transition)
	projection := ProjectTelemetryBytesPerDay(policy, int64(len(heartbeatBody)+1), int64(len(transitionBody)+1), 0)
	report.ProjectedMonitorBytesDay = projection.MonitorBytes
	report.ProjectedTelemetryBytesDay = projection.TotalBytes
	add("compact-monitor-budget", report.ProjectedMonitorBytesDay <= thresholds.TargetMonitorBytesDay,
		fmt.Sprintf("projection=%dB/day target=%dB/day hard=%dB/day", report.ProjectedMonitorBytesDay, thresholds.TargetMonitorBytesDay, thresholds.MaximumMonitorBytesDay))
	add("total-telemetry-budget", report.ProjectedTelemetryBytesDay <= thresholds.MaximumMonitorBytesDay,
		fmt.Sprintf("projection=%dB/day hard=%dB/day", report.ProjectedTelemetryBytesDay, thresholds.MaximumMonitorBytesDay))

	digestA := CommandShapeDigest("/usr/bin/go", 3)
	digestB := CommandShapeDigest("/usr/bin/go", 3)
	add("command-privacy", digestA == digestB && len(digestA) == 71, "digest covers executable identity and argument count only")

	report.SelectorBenchmark = benchmarkWorkSelector(limits, thresholds.MaximumSelectorP95US)
	report.RuntimeMilliseconds = time.Since(started).Milliseconds()
	add("evaluation-runtime", report.RuntimeMilliseconds <= thresholds.MaximumEvaluationMS,
		fmt.Sprintf("runtime budget=%dms; measured runtime is reported separately", thresholds.MaximumEvaluationMS))
	sort.SliceStable(report.Scenarios, func(i, j int) bool { return report.Scenarios[i].ID < report.Scenarios[j].ID })
	for _, scenario := range report.Scenarios {
		if scenario.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	report.ScenarioCount = len(report.Scenarios)
	report.OK = report.Failed == 0 && report.SelectorBenchmark.Pass
	digestInput, _ := json.Marshal(struct {
		Scenarios                  []WorkEvaluationScenario
		ClassWeights               []WorkClassWeight
		ProjectedMonitorBytesDay   int64
		ProjectedTelemetryBytesDay int64
		MaximumWorkEventBytes      int
		Thresholds                 WorkEvaluationThresholds
	}{report.Scenarios, report.ClassWeights, report.ProjectedMonitorBytesDay, report.ProjectedTelemetryBytesDay, report.MaximumWorkEventBytes, report.Thresholds})
	digest := sha256.Sum256(digestInput)
	report.DeterministicDigest = hex.EncodeToString(digest[:])
	if report.ProjectedMonitorBytesDay > thresholds.TargetMonitorBytesDay {
		report.ReviewSignals = append(report.ReviewSignals, "monitor telemetry projection exceeds the 50% target headroom")
	}
	if report.ProjectedTelemetryBytesDay > thresholds.MaximumMonitorBytesDay {
		report.ReviewSignals = append(report.ReviewSignals, "total telemetry projection exceeds the hard daily budget")
	}
	if !report.SelectorBenchmark.Pass {
		report.ReviewSignals = append(report.ReviewSignals, "selector overhead benchmark exceeds the under-lock p95 budget")
	}
	return report
}

var selectorBenchmarkSink int

func benchmarkWorkSelector(limits WorkLimits, budgetMicros int64) WorkSelectorBenchmark {
	const iterations = 4096
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	waiters := make([]WorkWaiterRecord, workSelectorScanLimit)
	for index := range waiters {
		waiters[index] = WorkWaiterRecord{
			OperationID: fmt.Sprintf("%032x", index+1),
			Weight:      limits.Capacity,
			QueuedAt:    now.Add(time.Duration(index) * time.Millisecond),
		}
	}
	durationsNanos := make([]int64, 0, iterations)
	maximumNanos := int64(0)
	for range iterations {
		started := time.Now()
		selection := selectWorkWaiter(waiters, max(0, limits.Capacity-1), limits.Capacity, now, workGreenExpressWindow{}, 0)
		elapsedNanos := time.Since(started).Nanoseconds()
		durationsNanos = append(durationsNanos, elapsedNanos)
		maximumNanos = max(maximumNanos, elapsedNanos)
		selectorBenchmarkSink += len(selection.DecisionReason)
	}
	p95Nanos := durationStats(durationsNanos).P95MS
	p95Micros := (p95Nanos + int64(time.Microsecond) - 1) / int64(time.Microsecond)
	maximumMicros := (maximumNanos + int64(time.Microsecond) - 1) / int64(time.Microsecond)
	return WorkSelectorBenchmark{
		Iterations: iterations, QueueDepth: len(waiters), P95Microseconds: p95Micros,
		MaxMicroseconds: maximumMicros, BudgetMicroseconds: budgetMicros, Pass: p95Micros < budgetMicros,
	}
}
