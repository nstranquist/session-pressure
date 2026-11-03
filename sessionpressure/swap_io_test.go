package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() {
	// Constructors default to the native kernel counter. A zero counter never
	// forms a baseline, so live host swap cannot leak into any fixture; tests
	// that exercise swap install an explicit source.
	defaultSwapIOSource = func(context.Context) (swapIOCounter, error) { return swapIOCounter{}, nil }
	defaultFreePercentSource = nil
}

func TestSwapIORateBetweenFailsClosedAndConvertsUnits(t *testing.T) {
	previous := swapIOCounter{SwapInBytes: 10 << 20, SwapOutBytes: 20 << 20}
	current := swapIOCounter{SwapInBytes: 40 << 20, SwapOutBytes: 920 << 20}
	rate, ok := swapIORateBetween(previous, current, 30*time.Second)
	if !ok || rate.InMBPerSecond != 1 || rate.OutMBPerSecond != 30 || rate.OutBytes != 900<<20 {
		t.Fatalf("rate=%+v ok=%v", rate, ok)
	}
	for name, test := range map[string]struct {
		previous, current swapIOCounter
		window            time.Duration
	}{
		"no baseline":       {swapIOCounter{}, current, time.Second},
		"counter regressed": {current, previous, time.Second},
		"zero window":       {previous, current, 0},
	} {
		if rate, ok := swapIORateBetween(test.previous, test.current, test.window); ok {
			t.Fatalf("%s produced a rate %+v", name, rate)
		}
	}
	if got := saturatingMulUint64(math.MaxUint64/2, 4); got != math.MaxUint64 {
		t.Fatalf("saturating multiply=%d", got)
	}
}

func TestSamplerSwapIORequiresBaselineAndResetsOnError(t *testing.T) {
	at := time.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC)
	counters := []swapIOCounter{{SwapOutBytes: 1 << 30}, {SwapOutBytes: 1<<30 + 90*32<<20}}
	index := 0
	var failNext bool
	sampler := &Sampler{now: func() time.Time { return at }}
	sampler.swapIOSource = func(context.Context) (swapIOCounter, error) {
		if failNext {
			return swapIOCounter{}, errors.New("host_statistics64 failed")
		}
		value := counters[min(index, len(counters)-1)]
		index++
		return value, nil
	}
	if _, ok := sampler.sampleSwapIO(context.Background()); ok {
		t.Fatal("first sample must only establish a baseline")
	}
	at = at.Add(90 * time.Second)
	rate, ok := sampler.sampleSwapIO(context.Background())
	if !ok || rate.OutMBPerSecond != 32 {
		t.Fatalf("rate=%+v ok=%v", rate, ok)
	}
	snapshot := Snapshot{}
	applySwapIORate(&snapshot, rate, ok)
	if !snapshot.SwapIOAvailable || snapshot.SwapOutMBPerSecond != 32 || snapshot.SwapIOWindowMS != 90_000 {
		t.Fatalf("snapshot swap projection=%+v", snapshot)
	}
	failNext = true
	if _, ok := sampler.sampleSwapIO(context.Background()); ok {
		t.Fatal("source error must not report a rate")
	}
	if sampler.swapIOLast != (swapIOCounter{}) {
		t.Fatal("source error must drop the baseline so recovery cannot span the outage")
	}
}

func TestEvaluateSwapOutIsAThroughputWarningNeverAMemoryLevel(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	quiet := Snapshot{FreePercent: 40}
	for _, test := range []struct {
		name    string
		rate    float64
		avail   bool
		latched bool
		level   Level
		reason  string
	}{
		{"unavailable is silent", 500, false, true, LevelNormal, ""},
		{"below warning", 7.9, true, false, LevelNormal, ""},
		{"warning", 8, true, false, LevelWarning, "swap-out 8.0 MB/s >= warning"},
		{"thrash caps at warning", 607, true, false, LevelWarning, "memory thrash"},
		{"latched below thrash", 20, true, true, LevelWarning, "swap thrash latched"},
	} {
		snapshot := quiet
		snapshot.SwapIOAvailable, snapshot.SwapOutMBPerSecond, snapshot.SwapThrashLatched = test.avail, test.rate, test.latched
		got := Evaluate(snapshot, policy)
		if got.Level != test.level {
			t.Fatalf("%s: level=%s want=%s reasons=%v", test.name, got.Level, test.level, got.Reasons)
		}
		if test.reason != "" && (len(got.Reasons) == 0 || !strings.Contains(got.Reasons[0], test.reason)) {
			t.Fatalf("%s: reasons=%v", test.name, got.Reasons)
		}
		// The memory-only ladder (agent launch, automatic relief) never sees it.
		if memory := EvaluateMemoryPressure(snapshot, policy); memory.Level != LevelNormal {
			t.Fatalf("%s: memory ladder level=%s reasons=%v", test.name, memory.Level, memory.Reasons)
		}
	}
	// Live thrash must not corroborate sticky swap use into critical: only
	// free-memory/agent evidence may authorize automatic relief.
	sticky := quiet
	sticky.SwapUsedMB = policy.Thresholds.SwapCriticalMB + 1
	sticky.SwapIOAvailable, sticky.SwapOutMBPerSecond = true, 500
	if got := Evaluate(sticky, policy); got.Level.AtLeast(LevelCritical) {
		t.Fatalf("thrash plus sticky swap level=%s reasons=%v; must not reach critical", got.Level, got.Reasons)
	}
}

func TestSwapThrashLatchIsHystereticAndFailsOpen(t *testing.T) {
	thresholds := DefaultPolicy(16 * 1024).Thresholds
	var latch swapThrashLatch
	step := func(rate float64, avail bool) bool {
		snapshot := Snapshot{SwapIOAvailable: avail, SwapOutMBPerSecond: rate}
		latch.observe(&snapshot, thresholds)
		return snapshot.SwapThrashLatched
	}
	for i, test := range []struct {
		rate  float64
		avail bool
		want  bool
	}{
		{69, true, false},   // one hot sample is a burst, not thrash
		{20, true, false},   // the band resets the hot streak
		{69, true, false},   // hot 1
		{607, true, true},   // hot 2 enters
		{20, true, true},    // the band holds the verdict
		{10, true, true},    // calm 1
		{40, true, true},    // hot resets calm
		{10, true, true},    // calm 1
		{16, true, false},   // calm 2 (release is inclusive) leaves
		{500, false, false}, // unavailable evidence clears state
		{500, true, false},  // and the streak restarts from zero
	} {
		if got := step(test.rate, test.avail); got != test.want {
			t.Fatalf("step %d rate=%.0f avail=%v latched=%v want=%v", i, test.rate, test.avail, got, test.want)
		}
	}
}

func TestSwapThrashDimensionNeverBlocksAgentsOrHeavyWorkByLevel(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.EnforceAdmission = true
	snapshot := Snapshot{FreePercent: 40, HostCPUAvailable: true, HostCPUPercent: 30, SwapIOAvailable: true, SwapOutMBPerSecond: 607, SwapThrashLatched: true}
	work := AdmissionForWorkSnapshot(snapshot, policy, "test")
	if !work.Allowed || work.Dimension != "swap" || work.Level != LevelWarning {
		t.Fatalf("work admission=%+v", work)
	}
	if work.Controller == nil || !work.Controller.SwapThrash || work.Controller.BlockWork || work.Controller.BlockAgentLaunch {
		t.Fatalf("controller=%+v", work.Controller)
	}
	launch := AdmissionForAgentLaunchSnapshot(snapshot, policy, "test")
	if !launch.Allowed || !strings.Contains(launch.Warning, "swap-out pressure observed") {
		t.Fatalf("agent launch admission=%+v", launch)
	}
	// A concurrent CPU breach keeps the CPU dimension and its latch evidence.
	snapshot.HostCPUPercent = policy.Thresholds.HostCPUWarningPercent
	if cpu := AdmissionForSnapshot(snapshot, policy, "test"); cpu.Dimension != "cpu" {
		t.Fatalf("CPU+swap dimension=%q", cpu.Dimension)
	}
}

func swapThrashAdmission(latched bool) Admission {
	snapshot := Snapshot{Level: LevelWarning, FreePercent: 40, SwapIOAvailable: true, SwapOutMBPerSecond: 300, SwapThrashLatched: latched}
	return Admission{Allowed: true, Level: LevelWarning, Dimension: "swap", Reasons: []string{"swap-out 300.0 MB/s >= thrash 32.0 MB/s (memory thrash)"}, Snapshot: &snapshot}
}

func TestSwapThrashDeratesNewHeavyWorkWithProgressGuarantee(t *testing.T) {
	limits := defaultWorkLimits(10) // capacity 8, build 5, test 3, thrash capacity 5
	if limits.SwapThrashCapacity != 5 {
		t.Fatalf("default thrash capacity=%d", limits.SwapThrashCapacity)
	}
	for _, test := range []struct {
		name      string
		class     WorkClass
		used      int
		ownsLease bool
		enforced  bool
		latched   bool
		thrashCap int
		allowed   bool
		label     string
	}{
		{"lone build fits", WorkClassBuild, 0, false, true, true, 0, true, SwapThrashAdmittedDecision},
		{"second build serializes", WorkClassBuild, 3, false, true, true, 0, false, SwapThrashDeferredDecision},
		{"test beside light work fits", WorkClassTest, 2, false, true, true, 0, true, SwapThrashAdmittedDecision},
		{"test behind a build waits", WorkClassTest, 5, false, true, true, 0, false, SwapThrashDeferredDecision},
		{"owned lease is not double counted", WorkClassBuild, 5, true, true, true, 0, true, SwapThrashAdmittedDecision},
		{"lone heavy op always runs", WorkClassBuild, 0, false, true, true, 2, true, SwapThrashAdmittedDecision},
		{"observe profile is report-only", WorkClassBuild, 3, false, false, true, 0, true, ""},
		{"released latch does not derate", WorkClassBuild, 3, false, true, false, 0, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateLimits := limits
			if test.thrashCap > 0 {
				gateLimits.SwapThrashCapacity = test.thrashCap
			}
			gate := &workAdmissionGate{
				limits: gateLimits, class: test.class, swapThrashEnforced: test.enforced,
				warningCapacityStatus: func() (WorkStatus, bool) {
					return WorkStatus{Used: test.used, Capacity: gateLimits.Capacity}, true
				},
			}
			decision := gate.observe(swapThrashAdmission(test.latched), false, test.ownsLease)
			if decision.Allowed != test.allowed {
				t.Fatalf("allowed=%v want=%v decision=%+v", decision.Allowed, test.allowed, decision)
			}
			if label := gate.decisionLabel(); label != test.label {
				t.Fatalf("label=%q want=%q", label, test.label)
			}
			if !decision.Allowed && (decision.Dimension != "swap" || !strings.Contains(decision.Reason, "thrash_capacity=5")) {
				t.Fatalf("deferral lacks typed evidence: %+v", decision)
			}
		})
	}
	// Unknown coordinator status must fail open, never into a deferral.
	gate := &workAdmissionGate{limits: limits, class: WorkClassBuild, swapThrashEnforced: true,
		warningCapacityStatus: func() (WorkStatus, bool) { return WorkStatus{}, false }}
	if decision := gate.observe(swapThrashAdmission(true), false, false); !decision.Allowed {
		t.Fatalf("status outage deferred work: %+v", decision)
	}
}

func TestSwapThrashPolicyValidationAndLegacyFill(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	policy.WorkLimits.SwapThrashCapacity = policy.WorkLimits.Capacity + 1
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "swap_thrash_capacity") {
		t.Fatalf("oversized thrash capacity validated: %v", err)
	}
	policy = DefaultPolicy(16 * 1024)
	policy.Thresholds.SwapOutReleaseMBPerSecond = policy.Thresholds.SwapOutThrashMBPerSecond
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "release < thrash") {
		t.Fatalf("release >= thrash validated: %v", err)
	}
	legacy := normalizeWorkLimits(WorkLimits{Capacity: 8, BuildWeight: 5})
	if legacy.SwapThrashCapacity != 5 {
		t.Fatalf("legacy limits thrash capacity=%d", legacy.SwapThrashCapacity)
	}
}

func TestLegacyPolicyFillsSwapOutThresholdsAndValidatesOrder(t *testing.T) {
	dir := t.TempDir()
	policy := DefaultPolicy(16 * 1024)
	policy.Thresholds.SwapOutWarningMBPerSecond, policy.Thresholds.SwapOutThrashMBPerSecond, policy.Thresholds.SwapOutReleaseMBPerSecond = 0, 0, 0
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := LoadPolicy(path, 16*1024)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Thresholds.SwapOutWarningMBPerSecond != 8 || loaded.Thresholds.SwapOutThrashMBPerSecond != 32 || loaded.Thresholds.SwapOutReleaseMBPerSecond != 16 {
		t.Fatalf("legacy swap-out thresholds=%+v", loaded.Thresholds)
	}
	loaded.Thresholds.SwapOutWarningMBPerSecond = 32
	if err := loaded.Validate(); err == nil || !strings.Contains(err.Error(), "swap-out rate") {
		t.Fatalf("inverted swap-out thresholds validated: %v", err)
	}
}

type swapThrashFixture struct {
	observer *DiskWriteObserver
	at       time.Time
	device   uint64
	swap     uint64
	process  uint64
}

func newSwapThrashFixture(t *testing.T) *swapThrashFixture {
	fixture := &swapThrashFixture{
		observer: NewDiskWriteObserver(t.TempDir(), defaultDiskWritePolicy()),
		at:       time.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC),
		swap:     1 << 30,
	}
	fixture.observer.store = nil
	fixture.observer.deviceSource = func(context.Context) (diskDeviceCounter, error) {
		return diskDeviceCounter{BytesWritten: fixture.device, DeviceCount: 1, Identity: "disk0", Source: "test"}, nil
	}
	fixture.observer.processSource = func(context.Context) (diskProcessSnapshot, error) {
		return diskProcessSnapshot{TotalPIDCount: 1, AccessibleCount: 1, Counters: []diskProcessCounter{
			{PID: 7, StartID: 9, Executable: "sqlite3", BytesWritten: fixture.process},
		}}, nil
	}
	fixture.observer.swapSource = func(context.Context) (swapIOCounter, error) {
		return swapIOCounter{SwapOutBytes: fixture.swap}, nil
	}
	return fixture
}

func (fixture *swapThrashFixture) run(samples int, deviceStep, swapStep, processStep uint64) DiskWriteReport {
	var report DiskWriteReport
	for index := 0; index < samples; index++ {
		report, _, _ = fixture.observer.Sample(context.Background(), fixture.at)
		fixture.at = fixture.at.Add(15 * time.Second)
		fixture.device += deviceStep
		fixture.swap += swapStep
		fixture.process += processStep
	}
	return report
}

func TestDiskWriteSwapThrashIsAttributedAndNeverTrainsBaseline(t *testing.T) {
	// 62 samples cover the 14-minute rolling gate, so learning would normally
	// accept every window into the quiet baseline.
	thrash := newSwapThrashFixture(t)
	report := thrash.run(62, 100<<20, 80<<20, 5<<20)
	summary := report.Summary
	if summary.TopWriter == nil || summary.TopWriter.Executable != diskWriteSwapExecutable || summary.TopWriter.Category != "system" {
		t.Fatalf("top writer=%+v; swap must be the attributed dominant writer", summary.TopWriter)
	}
	if len(report.Writers) == 0 || report.Writers[0].OwnerKind != "kernel" || report.Writers[0].PID != 0 {
		t.Fatalf("swap writer identity=%+v", report.Writers)
	}
	if summary.SwapShare15m != 0.8 || summary.SwapOutBytes15m == 0 {
		t.Fatalf("swap share=%v bytes=%d", summary.SwapShare15m, summary.SwapOutBytes15m)
	}
	if !containsString(summary.Reasons, "swap_dominant") || !containsString(summary.Reasons, "baseline_excluded_swap") {
		t.Fatalf("reasons=%v", summary.Reasons)
	}
	if count := thrash.observer.baselines["uncoordinated"].Count; count != 0 {
		t.Fatalf("swap thrash trained %d baseline samples", count)
	}
	if thrash.observer.currentHour.SwapOutBytes == 0 || thrash.observer.currentHour.BaselineEpoch != diskWriteBaselineEpoch {
		t.Fatalf("hour record swap=%d epoch=%d", thrash.observer.currentHour.SwapOutBytes, thrash.observer.currentHour.BaselineEpoch)
	}

	control := newSwapThrashFixture(t)
	control.run(62, 100<<20, 10<<20, 5<<20)
	if count := control.observer.baselines["uncoordinated"].Count; count == 0 {
		t.Fatal("minor swap below the dominance share must still train the quiet baseline")
	}
}

func TestDiskWriteSwapDeltaIsClampedToDeviceDelta(t *testing.T) {
	fixture := newSwapThrashFixture(t)
	report := fixture.run(3, 100, 400, 0)
	if report.Summary.SwapShare15m != 1 || report.Summary.SwapOutBytes15m != report.Summary.Window15mBytes {
		t.Fatalf("unclamped swap: share=%v swap=%d window=%d", report.Summary.SwapShare15m, report.Summary.SwapOutBytes15m, report.Summary.Window15mBytes)
	}
}

func TestDiskWriteSwapCounterOutageIsReportedAndRebased(t *testing.T) {
	fixture := newSwapThrashFixture(t)
	fixture.run(2, 100, 50, 0)
	fixture.observer.swapSource = func(context.Context) (swapIOCounter, error) { return swapIOCounter{}, errors.New("down") }
	report := fixture.run(1, 100, 50, 0)
	if !containsString(report.Summary.Reasons, "swap_counter_unavailable") {
		t.Fatalf("reasons=%v", report.Summary.Reasons)
	}
	if fixture.observer.previousSwap != (swapIOCounter{}) {
		t.Fatal("outage must drop the swap baseline so recovery cannot attribute the gap")
	}
}

func TestDiskWriteRestoreDropsPreEpochBaselinesButKeepsTraffic(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	saturated := diskHistogramRecord{Count: 500, Buckets: []diskHistogramBucket{{Index: 35, Count: 500}}, FirstAt: now.Add(-2 * time.Hour), LastAt: now.Add(-90 * time.Minute)}
	legacy := diskWriteHistoryRecord{
		SchemaVersion: 1, ModelVersion: diskWriteModelVersion, Hour: now.Add(-2 * time.Hour).Truncate(time.Hour),
		BytesWritten: 7_000, SampleCount: 240, Contexts: map[string]diskHistogramRecord{"uncoordinated": saturated},
		Executables: []diskExecutableHistory{{Executable: "python3.12", TotalBytes: 9, Histogram: saturated}},
	}
	current := diskWriteHistoryRecord{
		SchemaVersion: 1, ModelVersion: diskWriteModelVersion, BaselineEpoch: diskWriteBaselineEpoch, Hour: now.Add(-time.Hour).Truncate(time.Hour),
		BytesWritten: 3_000, SwapOutBytes: 1_000, SampleCount: 240,
		Contexts: map[string]diskHistogramRecord{"uncoordinated": {Count: 7, Buckets: []diskHistogramBucket{{Index: 10, Count: 7}}, FirstAt: now.Add(-time.Hour), LastAt: now.Add(-50 * time.Minute)}},
	}
	store := NewDiskWriteStore(dir)
	for _, record := range []diskWriteHistoryRecord{legacy, current} {
		if err := store.appendRecord(record); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := legacy
	checkpoint.Hour = now.Truncate(time.Hour)
	checkpoint.LastSampleAt = now.Add(-15 * time.Second)
	checkpoint.DeviceIdentity = "disk0"
	checkpoint.BytesWritten = 500
	if err := store.writeCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}

	observer := NewDiskWriteObserver(dir, defaultDiskWritePolicy())
	observer.mu.RLock()
	defer observer.mu.RUnlock()
	if baseline := observer.baselines["uncoordinated"]; baseline == nil || baseline.Count != 7 {
		t.Fatalf("baseline=%+v; only epoch-%d histograms may merge", baseline, diskWriteBaselineEpoch)
	}
	if _, ok := observer.executableModels["python3.12"]; ok {
		t.Fatal("pre-epoch executable models must be dropped")
	}
	// 7,000 pre-epoch + 3,000 current + 500 stale-epoch checkpoint.
	if total := observer.windowBytesLocked(now.Add(-diskWriteHistoryWindow), false); total != 10_500 {
		t.Fatalf("restored traffic=%d; pre-epoch history and checkpoint must still count", total)
	}
	if observer.currentHour == nil || len(observer.currentHour.Contexts) != 0 || observer.currentHour.BaselineEpoch != diskWriteBaselineEpoch {
		t.Fatalf("checkpoint restore=%+v; stale-epoch partial hour must drop histograms", observer.currentHour)
	}
	history, _, err := store.readHistory(10, now.Add(-24*time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	var swapTotal uint64
	for _, point := range history {
		swapTotal += point.SwapOutBytes
	}
	if swapTotal != 1_000 {
		t.Fatalf("history swap total=%d", swapTotal)
	}
}

func TestSamplerFreePercentPrefersNativeAndFallsBackToMemoryPressure(t *testing.T) {
	runner := fixtureRunner{pressure: "System-wide memory free percentage: 37%\n"}
	sampler := &Sampler{runner: runner, freePercentSource: func() (int, error) { return 41, nil }}
	if got, err := sampler.freePercent(context.Background()); err != nil || got != 41 {
		t.Fatalf("native free=%d err=%v", got, err)
	}
	for name, source := range map[string]func() (int, error){
		"error":        func() (int, error) { return 0, errors.New("sysctl failed") },
		"out of range": func() (int, error) { return 101, nil },
		"absent":       nil,
	} {
		sampler.freePercentSource = source
		if got, err := sampler.freePercent(context.Background()); err != nil || got != 37 {
			t.Fatalf("%s: fallback free=%d err=%v", name, got, err)
		}
	}
}

func TestWorkProgressDeduplicatesVolatileFieldsAndBacksOff(t *testing.T) {
	now := time.Date(2026, 11, 2, 23, 0, 0, 0, time.UTC)
	var out strings.Builder
	reporter := newWorkProgressReporter(WorkProgressHuman, &out, "op", WorkClassTest, 3, now)
	reporter.now = func() time.Time { return now }
	lines := func() int { return strings.Count(out.String(), "\n") }
	// 5 minutes of 2s polls whose used capacity and reason duration churn.
	for second := 0; second < 300; second += 2 {
		now = time.Date(2026, 11, 2, 23, 0, second, 0, time.UTC)
		reporter.emit(WorkProgress{Stage: "waiting", Blocker: WorkBlockerCapacity, QueuePosition: 1, QueueDepth: 4,
			Used: 5 + second%3, Capacity: 8, Reason: fmt.Sprintf("finite express-build lease has run %ds", 900+second)})
	}
	// first line + heartbeats at 30s, 90s, 210s (30, 60, 120 cadence).
	if got := lines(); got != 4 {
		t.Fatalf("unchanged wait printed %d lines; want 4:\n%s", got, out.String())
	}
	now = now.Add(2 * time.Second)
	reporter.emit(WorkProgress{Stage: "waiting", Blocker: WorkBlockerReservation, QueuePosition: 1, QueueDepth: 4})
	if got := lines(); got != 5 {
		t.Fatalf("blocker change must print immediately; lines=%d", got)
	}
}

// A coordinator state-lock timeout inside a long --wait budget is contention
// (a holder paged out under thrash), not a failure: nothing was mutated, so
// the wait loop retries and reports the typed state_lock blocker.
func TestWaitAcquireRetriesStateLockTimeoutWithinBudget(t *testing.T) {
	previousFallback, previousRetry := workStateLockFallback, workStateLockRetryInterval
	workStateLockFallback, workStateLockRetryInterval = 100*time.Millisecond, 20*time.Millisecond
	defer func() { workStateLockFallback, workStateLockRetryInterval = previousFallback, previousRetry }()

	coordinator := NewWorkCoordinator(t.TempDir(), testWorkLimits())
	if _, err := coordinator.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	lockPath := coordinator.statePath() + ".lock"
	held := fmt.Sprintf(`{"pid":%d,"timestamp":%d,"token":"foreign-holder"}`, os.Getpid(), time.Now().UnixMilli())
	if err := os.WriteFile(lockPath, []byte(held), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(450 * time.Millisecond)
		_ = os.Remove(lockPath)
	}()
	var blockers []WorkBlocker
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	operationID, err := NewWorkOperationID()
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := coordinator.WaitAcquireOperation(ctx, WorkClassTest, operationID, func(_ WorkStatus, blocker WorkBlocker) {
		blockers = append(blockers, blocker)
	})
	if err != nil {
		t.Fatalf("lock stall inside wait budget failed the operation: %v", err)
	}
	defer func() { _ = lease.Release(context.Background()) }()
	if len(blockers) == 0 || blockers[0] != WorkBlockerStateLock {
		t.Fatalf("blockers=%v; want state_lock first", blockers)
	}

	// An exhausted budget still fails, and the cause stays inspectable.
	if err := os.WriteFile(lockPath, []byte(held), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lockPath)
	short, shortCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer shortCancel()
	other, err := NewWorkOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.WaitAcquireOperation(short, WorkClassTest, other, nil); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exhausted budget err=%v; want deadline exceeded", err)
	}
}
