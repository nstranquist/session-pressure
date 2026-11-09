package sessionpressure

import (
	"context"
	"testing"
	"time"
)

// On 2026-11-09 every hour above ~89 GB had no named writer, and the
// hours that had one summed a rolling 15-minute window each sample (13:00:
// 59 GB attributed against 18 GB written). Hourly attribution must cover
// burst windows that baseline training rejects, and must equal the exact
// per-sample process deltas.
func TestDiskWriteHourWritersCoverBurstWindowsExactly(t *testing.T) {
	observer := NewDiskWriteObserver(t.TempDir(), defaultDiskWritePolicy())
	observer.store = nil
	var device, git, codex uint64
	observer.deviceSource = func(context.Context) (diskDeviceCounter, error) {
		return diskDeviceCounter{BytesWritten: device, DeviceCount: 1, Identity: "disk0", Source: "test"}, nil
	}
	observer.processSource = func(context.Context) (diskProcessSnapshot, error) {
		return diskProcessSnapshot{TotalPIDCount: 2, AccessibleCount: 2, Counters: []diskProcessCounter{
			{PID: 7, StartID: 1, Executable: "git", BytesWritten: git},
			{PID: 8, StartID: 2, Executable: "codex", BytesWritten: codex},
		}}, nil
	}
	observer.swapSource = func(context.Context) (swapIOCounter, error) { return swapIOCounter{}, nil }
	start := time.Date(2026, 11, 9, 16, 0, 0, 0, time.UTC)
	observer.Sample(context.Background(), start)
	const samples = 10
	for i := 1; i <= samples; i++ {
		// Each sample is a burst: baseline training rejects all of them.
		device += diskWriteBurstBytes
		git += 1_000_000
		codex += 10_000
		report, _, _ := observer.Sample(context.Background(), start.Add(time.Duration(i)*15*time.Second))
		if report.Summary.State != DiskWriteStateHigh {
			t.Fatalf("sample %d state %s, want high", i, report.Summary.State)
		}
		if report.Summary.BaselineSamples != 0 {
			t.Fatalf("sample %d trained the baseline on a burst", i)
		}
	}
	record, ok := observer.currentHourRecordLocked()
	if !ok {
		t.Fatal("no current hour record")
	}
	if len(record.Executables) != 0 {
		t.Fatalf("burst windows produced training histograms: %+v", record.Executables)
	}
	totals := map[string]uint64{}
	for _, writer := range record.Writers {
		totals[writer.Executable] = writer.TotalBytes
	}
	if totals["git"] != samples*1_000_000 || totals["codex"] != samples*10_000 {
		t.Fatalf("hour writers = %+v, want exact per-sample sums", record.Writers)
	}
	if record.AttributedBytes != samples*1_010_000 {
		t.Fatalf("attributed bytes = %d, want %d", record.AttributedBytes, samples*1_010_000)
	}
	if record.Writers[0].Executable != "git" {
		t.Fatalf("writers not ordered by bytes: %+v", record.Writers)
	}
}

// History reads prefer exact writers and fall back to the old training
// totals only for hours written before them.
func TestDiskWriteHistoryPrefersExactWriters(t *testing.T) {
	store := NewDiskWriteStore(t.TempDir())
	hour := time.Date(2026, 11, 9, 16, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return hour.Add(30 * time.Minute) }
	legacy := diskWriteHistoryRecord{Hour: hour.Add(-time.Hour), BytesWritten: 10, Contexts: map[string]diskHistogramRecord{},
		Executables: []diskExecutableHistory{{Executable: "catalog-serve", TotalBytes: 900}}}
	exact := diskWriteHistoryRecord{Hour: hour, BytesWritten: 100, AttributedBytes: 70, Contexts: map[string]diskHistogramRecord{},
		Executables: []diskExecutableHistory{{Executable: "inflated", TotalBytes: 6000}},
		Writers:     []DiskWriteHistoryExecutable{{Executable: "git", TotalBytes: 60}, {Executable: "codex", TotalBytes: 10}}}
	for _, record := range []diskWriteHistoryRecord{legacy, exact} {
		if err := store.appendRecord(record); err != nil {
			t.Fatal(err)
		}
	}
	points, _, err := store.ReadHistoryWithExecutables(10, hour.Add(-2*time.Hour), 8)
	if err != nil || len(points) != 2 {
		t.Fatalf("points=%+v err=%v", points, err)
	}
	byHour := map[time.Time]DiskWriteHistoryPoint{}
	for _, point := range points {
		byHour[point.Hour] = point
	}
	if got := byHour[hour]; got.AttributedBytes != 70 || len(got.Executables) != 2 || got.Executables[0].Executable != "git" {
		t.Fatalf("exact hour = %+v, want git first and 70 attributed", got)
	}
	if got := byHour[hour.Add(-time.Hour)]; len(got.Executables) != 1 || got.Executables[0].Executable != "catalog-serve" {
		t.Fatalf("legacy hour = %+v, want the training fallback", got)
	}
}
