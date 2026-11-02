package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
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

func TestEvaluateSwapOutRateIsLiveAndCappedAtRed(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	quiet := Snapshot{FreePercent: 40}
	for _, test := range []struct {
		name   string
		rate   float64
		avail  bool
		level  Level
		reason string
	}{
		{"unavailable is silent", 500, false, LevelNormal, ""},
		{"below warning", 7.9, true, LevelNormal, ""},
		{"warning", 8, true, LevelWarning, "swap-out 8.0 MB/s >= warning"},
		{"red thrash", 69, true, LevelRed, "memory thrash"},
	} {
		snapshot := quiet
		snapshot.SwapIOAvailable, snapshot.SwapOutMBPerSecond = test.avail, test.rate
		got := Evaluate(snapshot, policy)
		if got.Level != test.level {
			t.Fatalf("%s: level=%s want=%s reasons=%v", test.name, got.Level, test.level, got.Reasons)
		}
		if test.reason != "" && (len(got.Reasons) == 0 || !strings.Contains(got.Reasons[0], test.reason)) {
			t.Fatalf("%s: reasons=%v", test.name, got.Reasons)
		}
	}
	// Live thrash must not corroborate sticky swap use into critical: only
	// free-memory/agent evidence may authorize automatic relief.
	sticky := quiet
	sticky.SwapUsedMB = policy.Thresholds.SwapCriticalMB + 1
	sticky.SwapIOAvailable, sticky.SwapOutMBPerSecond = true, 500
	if got := Evaluate(sticky, policy); got.Level != LevelRed {
		t.Fatalf("thrash plus sticky swap level=%s reasons=%v; want red cap", got.Level, got.Reasons)
	}
}

func TestLegacyPolicyFillsSwapOutThresholdsAndValidatesOrder(t *testing.T) {
	dir := t.TempDir()
	policy := DefaultPolicy(16 * 1024)
	policy.Thresholds.SwapOutWarningMBPerSecond, policy.Thresholds.SwapOutRedMBPerSecond = 0, 0
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
	if loaded.Thresholds.SwapOutWarningMBPerSecond != 8 || loaded.Thresholds.SwapOutRedMBPerSecond != 32 {
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
