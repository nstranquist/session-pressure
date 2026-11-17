package sessionpressure

import (
	"context"
	"testing"
	"time"
)

// passHarness drives an observer on the resident cadence (15s ticks, 60s
// attribution pass) with one process that writes 1,000 bytes per tick and a
// kernel swap-out of 100 bytes per tick.
type passHarness struct {
	observer   *DiskWriteObserver
	device     uint64
	process    uint64
	swap       uint64
	passes     int
	start      time.Time
	deviceStep uint64
}

func newPassHarness(t *testing.T) *passHarness {
	t.Helper()
	harness := &passHarness{start: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC), deviceStep: 2_000}
	observer := NewDiskWriteObserver(t.TempDir(), defaultDiskWritePolicy())
	observer.store = nil
	observer.processPassInterval = diskWriteProcessPassInterval
	observer.deviceSource = func(context.Context) (diskDeviceCounter, error) {
		return diskDeviceCounter{BytesWritten: harness.device, DeviceCount: 1, Identity: "disk0", Source: "test"}, nil
	}
	observer.processSource = func(context.Context) (diskProcessSnapshot, error) {
		harness.passes++
		return diskProcessSnapshot{TotalPIDCount: 2, AccessibleCount: 1, Counters: []diskProcessCounter{{PID: 7, StartID: 9, Executable: "sqlite3", BytesWritten: harness.process}}}, nil
	}
	observer.swapSource = func(context.Context) (swapIOCounter, error) {
		return swapIOCounter{SwapOutBytes: harness.swap + 1}, nil
	}
	harness.observer = observer
	return harness
}

func (harness *passHarness) tick(at time.Duration) DiskWriteReport {
	report, _, _ := harness.observer.Sample(context.Background(), harness.start.Add(at))
	harness.device += harness.deviceStep
	harness.process += 1_000
	harness.swap += 100
	return report
}

func writerBytes(report DiskWriteReport, executable string) uint64 {
	for _, writer := range report.Writers {
		if writer.Executable == executable {
			return writer.WindowBytes
		}
	}
	return 0
}

// Quiet writes take one per-process pass a minute, not one per tick, and
// the pass attributes every byte of the minute, swap included.
func TestDiskWriteProcessPassRunsOncePerMinuteWithoutLosingBytes(t *testing.T) {
	harness := newPassHarness(t)
	var report DiskWriteReport
	for step := 0; step <= 8; step++ { // 0s .. 120s
		report = harness.tick(time.Duration(step) * 15 * time.Second)
	}
	if harness.passes != 3 { // 0s, 60s, 120s
		t.Fatalf("passes = %d over 9 ticks, want 3", harness.passes)
	}
	if got := writerBytes(report, "sqlite3"); got != 8_000 {
		t.Fatalf("sqlite3 window bytes = %d, want 8000 (no bytes lost between passes)", got)
	}
	if got := writerBytes(report, diskWriteSwapExecutable); got != 800 {
		t.Fatalf("swap window bytes = %d, want 800 (pending swap carried into the pass)", got)
	}
	for _, writer := range report.Writers {
		if writer.Executable == "sqlite3" && writer.BytesPerSecond != 1_000.0/15 {
			t.Fatalf("sqlite3 rate = %v, want the pass-window rate %v", writer.BytesPerSecond, 1_000.0/15)
		}
	}
	// A skipped tick still reports the last pass's coverage, not zeros.
	skipped := harness.tick(9 * 15 * time.Second)
	if harness.passes != 3 || skipped.Summary.TotalPIDCount != 2 || skipped.Summary.AccessiblePIDCount != 1 || !skipped.Summary.AttributionAvailable {
		t.Fatalf("skipped tick lost attribution coverage: passes=%d summary=%+v", harness.passes, skipped.Summary)
	}
	if !containsString(skipped.Summary.Reasons, "process_attribution_partial") {
		t.Fatalf("skipped tick dropped the partial-attribution reason: %v", skipped.Summary.Reasons)
	}
}

// A gap between passes rebases the process counters instead of crediting a
// writer with bytes from outside any valid window.
func TestDiskWriteProcessPassRebasesAcrossAGap(t *testing.T) {
	harness := newPassHarness(t)
	harness.tick(0)
	harness.tick(15 * time.Second)
	harness.process += 1 << 30    // written during the gap
	harness.tick(2 * time.Minute) // gap: no pass due yet by count, window invalid
	report := harness.tick(2*time.Minute + 15*time.Second)
	if got := writerBytes(report, "sqlite3"); got >= 1<<30 {
		t.Fatalf("gap bytes were attributed to a writer: %d", got)
	}
}

// Heavy writes name the writers on every tick.
func TestDiskWriteProcessPassRunsEveryTickWhenWritesAreHeavy(t *testing.T) {
	harness := newPassHarness(t)
	harness.tick(0)
	harness.deviceStep = diskWriteProcessPassBytes
	harness.tick(15 * time.Second)
	before := harness.passes
	harness.tick(30 * time.Second)
	harness.tick(45 * time.Second)
	if harness.passes != before+2 {
		t.Fatalf("heavy-write ticks took %d passes, want 2", harness.passes-before)
	}
}

// Live reports and other callers that do not set the interval keep a pass on
// every tick.
func TestDiskWriteProcessPassDefaultsToEveryTick(t *testing.T) {
	harness := newPassHarness(t)
	harness.observer.processPassInterval = 0
	for step := 0; step < 4; step++ {
		harness.tick(time.Duration(step) * 15 * time.Second)
	}
	if harness.passes != 4 {
		t.Fatalf("passes = %d, want 4", harness.passes)
	}
}
