package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiskWriteHistogramP99UsesBoundedLogBuckets(t *testing.T) {
	start := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	histogram := diskHistogram{}
	for index := 0; index < 98; index++ {
		histogram.add(1, start.Add(time.Duration(index)*time.Second))
	}
	histogram.add(1024, start.Add(98*time.Second))
	histogram.add(1024, start.Add(99*time.Second))
	if got := histogram.p99(); got != 2047 {
		t.Fatalf("p99 bucket=%d want=2047", got)
	}
}

func TestLegacyPolicyFillsDiskWriteObservationWithoutNotifications(t *testing.T) {
	dir := t.TempDir()
	policy := DefaultPolicy(16 * 1024)
	policy.DiskWrite = DiskWritePolicy{}
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, persisted, err := LoadPolicy(path, 16*1024)
	if err != nil || !persisted {
		t.Fatalf("load legacy policy: persisted=%v err=%v", persisted, err)
	}
	if !loaded.DiskWrite.Enabled || loaded.DiskWrite.NotificationsEnabled || loaded.DiskWrite.SampleIntervalSeconds != 15 || loaded.DiskWrite.Profile != DiskWriteProfileQuietAdaptiveV1 {
		t.Fatalf("unexpected migrated disk-write policy: %+v", loaded.DiskWrite)
	}
}

func TestDiskWriteObserverComputesResetSafeDeltasAndBoundedWriters(t *testing.T) {
	policy := defaultDiskWritePolicy()
	observer := NewDiskWriteObserver(t.TempDir(), policy)
	observer.store = nil
	devices := []diskDeviceCounter{
		{BytesWritten: 1_000, DeviceCount: 1, Identity: "disk0", Source: "test"},
		{BytesWritten: 31_000, DeviceCount: 1, Identity: "disk0", Source: "test"},
		{BytesWritten: 10, DeviceCount: 1, Identity: "disk0", Source: "test"},
	}
	processes := []diskProcessSnapshot{
		{TotalPIDCount: 1, AccessibleCount: 1, Counters: []diskProcessCounter{{PID: 7, StartID: 9, Executable: "sqlite3", BytesWritten: 100}}},
		{TotalPIDCount: 1, AccessibleCount: 1, Counters: []diskProcessCounter{{PID: 7, StartID: 9, Executable: "sqlite3", BytesWritten: 10_100}}},
		{TotalPIDCount: 1, AccessibleCount: 1, Counters: []diskProcessCounter{{PID: 7, StartID: 10, Executable: "sqlite3", BytesWritten: 50}}},
	}
	index := 0
	observer.deviceSource = func(context.Context) (diskDeviceCounter, error) { return devices[index], nil }
	observer.processSource = func(context.Context) (diskProcessSnapshot, error) {
		value := processes[index]
		index++
		return value, nil
	}
	start := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	initial, _, _ := observer.Sample(context.Background(), start)
	if initial.Summary.MeasurementWindowSeconds != 0 {
		t.Fatalf("initial counter baseline invented a measurement window: %+v", initial.Summary)
	}
	report, _, _ := observer.Sample(context.Background(), start.Add(15*time.Second))
	if report.Summary.CurrentBytesPerSecond != 2000 || report.Summary.Window15mBytes != 30_000 || report.AvailableCount != 1 {
		t.Fatalf("unexpected delta report: %+v", report)
	}
	if got := report.Writers[0]; got.Executable != "sqlite3" || got.Category != "database" || got.WindowBytes != 10_000 || got.PID != 7 || got.ProcessStartID != 9 {
		t.Fatalf("unexpected writer: %+v", got)
	}
	rebased, _, _ := observer.Sample(context.Background(), start.Add(30*time.Second))
	if rebased.Summary.CurrentBytesPerSecond != 0 || !containsString(rebased.Summary.Reasons, "counter_baseline_initialized") {
		t.Fatalf("counter reset was not safely rebased: %+v", rebased.Summary)
	}
}

func TestDiskWriteGapAndDeviceChangeRebaseWithoutRateSpike(t *testing.T) {
	observer := NewDiskWriteObserver(t.TempDir(), defaultDiskWritePolicy())
	observer.store = nil
	devices := []diskDeviceCounter{
		{BytesWritten: 1_000, DeviceCount: 1, Identity: "disk0", Source: "test"},
		{BytesWritten: 2_000, DeviceCount: 1, Identity: "disk0", Source: "test"},
		{BytesWritten: 50_000, DeviceCount: 1, Identity: "disk1", Source: "test"},
	}
	index := 0
	observer.deviceSource = func(context.Context) (diskDeviceCounter, error) {
		value := devices[index]
		index++
		return value, nil
	}
	observer.processSource = func(context.Context) (diskProcessSnapshot, error) { return diskProcessSnapshot{}, nil }
	start := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	observer.Sample(context.Background(), start)
	gap, _, _ := observer.Sample(context.Background(), start.Add(time.Minute))
	if gap.Summary.CurrentBytesPerSecond != 0 || gap.Summary.UnscoredGapBytes != 1_000 || !containsString(gap.Summary.Reasons, "sample_gap_rebased") {
		t.Fatalf("long sample gap was not explicitly rebased: %+v", gap.Summary)
	}
	changed, _, _ := observer.Sample(context.Background(), start.Add(75*time.Second))
	if changed.Summary.CurrentBytesPerSecond != 0 || changed.Summary.UnscoredGapBytes != 1_000 || !containsString(changed.Summary.Reasons, "counter_baseline_initialized") {
		t.Fatalf("device-set change created a rate or lost prior gap evidence: %+v", changed.Summary)
	}
}

func TestDiskWriteUnavailableSourcesFailClosed(t *testing.T) {
	observer := NewDiskWriteObserver(t.TempDir(), defaultDiskWritePolicy())
	observer.store = nil
	observer.deviceSource = func(context.Context) (diskDeviceCounter, error) {
		return diskDeviceCounter{}, errors.New("device denied")
	}
	observer.processSource = func(context.Context) (diskProcessSnapshot, error) {
		return diskProcessSnapshot{}, errors.New("process denied")
	}
	report, transition, alert := observer.Sample(context.Background(), time.Now().UTC())
	if report.Summary.State != DiskWriteStateUnavailable ||
		!containsString(report.Summary.Reasons, "device_counter_unavailable") ||
		!containsString(report.Summary.Reasons, "process_attribution_unavailable") ||
		len(report.Writers) != 0 || alert != nil {
		t.Fatalf("unavailable sources did not fail closed: report=%+v transition=%+v alert=%+v", report, transition, alert)
	}
}

func TestDiskWriteQuietModelRequiresSustainedConfidenceAndDoesNotSelfTrain(t *testing.T) {
	observer := NewDiskWriteObserver(t.TempDir(), defaultDiskWritePolicy())
	observer.store = nil
	start := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	histogram := &diskHistogram{}
	for index := 0; index < diskWriteConfidentSamples; index++ {
		histogram.add(1024, start.Add(-8*24*time.Hour).Add(time.Duration(index)*time.Second))
	}
	observer.baselines["uncoordinated"] = histogram
	writers := []DiskWriter{{DiskWriterSummary: DiskWriterSummary{Executable: "sqlite3", WindowBytes: 16_000}}}
	for index := 0; index < 41; index++ {
		state, accepted := observer.classifyLocked(start.Add(time.Duration(index)*15*time.Second), DiskWriteConfidenceConfident, 16, histogram.p99(), writers, 16_000)
		if accepted {
			t.Fatalf("anomalous sample %d was accepted into baseline", index)
		}
		if index == 40 && state != DiskWriteStateHigh {
			t.Fatalf("sustained high state=%s", state)
		}
	}
	summary := DiskWriteSummary{CapturedAt: start.Add(10 * time.Minute), State: DiskWriteStateHigh, Confidence: DiskWriteConfidenceConfident, BaselineRatio: 16, Window15mBytes: 16_000, TopWriter: &DiskWriterSummary{Executable: "sqlite3"}}
	observer.policy.NotificationsEnabled = true
	if alert := observer.alertLocked(summary); alert == nil || alert.TopWriter != "sqlite3" {
		t.Fatalf("expected first high alert, got %+v", alert)
	}
	if alert := observer.alertLocked(summary); alert != nil {
		t.Fatalf("duplicate incident alert: %+v", alert)
	}
}

func TestDiskWriteRecoveryNeverTrainsOpenIncident(t *testing.T) {
	observer := NewDiskWriteObserver(t.TempDir(), defaultDiskWritePolicy())
	observer.store = nil
	start := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	observer.lastState = DiskWriteStateHigh

	state, accepted := observer.classifyLocked(
		start,
		DiskWriteConfidenceConfident,
		3,
		1024,
		nil,
		0,
	)
	if state != DiskWriteStateHigh || accepted {
		t.Fatalf("open high incident state=%s accepted=%v; recovery must not train", state, accepted)
	}

	observer.recoverySince = start
	state, accepted = observer.classifyLocked(
		start.Add(diskWriteRecoveryDuration),
		DiskWriteConfidenceConfident,
		1,
		1024,
		nil,
		0,
	)
	if state != DiskWriteStateNormal || accepted {
		t.Fatalf("first recovered sample state=%s accepted=%v; prior incident must close before training resumes", state, accepted)
	}
}

func TestMergeLiveDiskWriteReportPreservesResidentModelAndLiveAttribution(t *testing.T) {
	resident := DiskWriteSummary{
		SchemaVersion:       DiskWriteSummarySchemaVersion,
		ModelVersion:        diskWriteModelVersion,
		CapturedAt:          time.Now().UTC().Add(-time.Minute),
		State:               DiskWriteStateNormal,
		Confidence:          DiskWriteConfidenceConfident,
		Context:             "test",
		Window15mBytes:      12_000,
		Bytes24h:            34_000,
		UnscoredGapBytes:    500,
		BaselineP99Bytes15m: 8_000,
		BaselineRatio:       1.5,
		BaselineSamples:     diskWriteConfidentSamples,
		BaselineAgeSeconds:  diskWriteConfidentAge.Seconds(),
		Reasons:             []string{"resident_model"},
	}
	top := DiskWriterSummary{Executable: "sqlite3", WindowBytes: 4_096}
	live := DiskWriteReport{
		Summary: DiskWriteSummary{
			SchemaVersion:            DiskWriteSummarySchemaVersion,
			ModelVersion:             diskWriteModelVersion,
			CapturedAt:               time.Now().UTC(),
			State:                    DiskWriteStateLearning,
			Confidence:               DiskWriteConfidenceLearning,
			Context:                  "uncoordinated",
			CurrentBytesPerSecond:    4_096,
			MeasurementWindowSeconds: 1,
			AttributionAvailable:     true,
			TopWriter:                &top,
			Reasons:                  []string{"rolling_window_incomplete", "process_attribution_partial"},
		},
		Writers:        []DiskWriter{{DiskWriterSummary: top, PID: 42, ProcessStartID: 7}},
		AvailableCount: 1,
		ReturnedCount:  1,
	}

	merged := MergeLiveDiskWriteReport(live, resident)
	if merged.Summary.State != DiskWriteStateNormal ||
		merged.Summary.Confidence != DiskWriteConfidenceConfident ||
		merged.Summary.Context != "test" ||
		merged.Summary.Window15mBytes != 12_000 ||
		merged.Summary.CurrentBytesPerSecond != 4_096 ||
		merged.Summary.TopWriter == nil ||
		merged.Summary.TopWriter.Executable != "sqlite3" {
		t.Fatalf("merged live report lost resident model or live attribution: %+v", merged)
	}
	if containsString(merged.Summary.Reasons, "rolling_window_incomplete") ||
		!containsString(merged.Summary.Reasons, "process_attribution_partial") {
		t.Fatalf("merged live reasons=%v", merged.Summary.Reasons)
	}
}

func TestDiskWriteConfidenceRecoveryAndDailyNotificationCap(t *testing.T) {
	start := time.Date(2026, 7, 22, 12, 0, 0, 0, time.Local)
	histogram := diskHistogram{Count: diskWriteLearningSamples, FirstAt: start.Add(-diskWriteLearningAge)}
	if got := diskWriteConfidenceFor(histogram, start); got != DiskWriteConfidenceProvisional {
		t.Fatalf("learning threshold confidence=%s", got)
	}
	histogram.Count = diskWriteConfidentSamples
	histogram.FirstAt = start.Add(-diskWriteConfidentAge)
	if got := diskWriteConfidenceFor(histogram, start); got != DiskWriteConfidenceConfident {
		t.Fatalf("confident threshold confidence=%s", got)
	}

	observer := NewDiskWriteObserver(t.TempDir(), defaultDiskWritePolicy())
	observer.store = nil
	observer.lastState = DiskWriteStateHigh
	state, _ := observer.classifyLocked(start, DiskWriteConfidenceConfident, 1, 100, nil, 0)
	if state != DiskWriteStateHigh {
		t.Fatalf("high state recovered without cooldown: %s", state)
	}
	state, _ = observer.classifyLocked(start.Add(diskWriteRecoveryDuration), DiskWriteConfidenceConfident, 1, 100, nil, 0)
	if state != DiskWriteStateNormal {
		t.Fatalf("high state did not recover after cooldown: %s", state)
	}

	observer.policy.NotificationsEnabled = true
	alerts := 0
	for index := 0; index < diskWriteAlertLimitPerDay+1; index++ {
		at := start.Add(time.Duration(index) * time.Minute)
		if observer.alertLocked(DiskWriteSummary{CapturedAt: at, State: DiskWriteStateHigh}) != nil {
			alerts++
		}
		observer.alertLocked(DiskWriteSummary{CapturedAt: at, State: DiskWriteStateNormal})
	}
	if alerts != diskWriteAlertLimitPerDay {
		t.Fatalf("daily notification count=%d want=%d", alerts, diskWriteAlertLimitPerDay)
	}
}

func TestDiskWriteAlertCapAndIncidentSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 7, 22, 12, 0, 0, 0, time.Local)
	policy := defaultDiskWritePolicy()
	policy.NotificationsEnabled = true
	observer := NewDiskWriteObserver(dir, policy)

	first := DiskWriteSummary{CapturedAt: start, State: DiskWriteStateHigh}
	if alert := observer.alertLocked(first); alert == nil {
		t.Fatal("first incident did not alert")
	}
	restarted := NewDiskWriteObserver(dir, policy)
	if alert := restarted.alertLocked(DiskWriteSummary{CapturedAt: start.Add(time.Minute), State: DiskWriteStateHigh}); alert != nil {
		t.Fatalf("restart repeated an open-incident alert: %+v", alert)
	}

	restarted.alertLocked(DiskWriteSummary{CapturedAt: start.Add(2 * time.Minute), State: DiskWriteStateNormal})
	for index := 1; index < diskWriteAlertLimitPerDay; index++ {
		at := start.Add(time.Duration(index+2) * time.Minute)
		if alert := restarted.alertLocked(DiskWriteSummary{CapturedAt: at, State: DiskWriteStateHigh}); alert == nil {
			t.Fatalf("incident %d did not alert", index+1)
		}
		restarted.alertLocked(DiskWriteSummary{CapturedAt: at.Add(30 * time.Second), State: DiskWriteStateNormal})
	}

	again := NewDiskWriteObserver(dir, policy)
	if alert := again.alertLocked(DiskWriteSummary{CapturedAt: start.Add(time.Hour), State: DiskWriteStateHigh}); alert != nil {
		t.Fatalf("restart exceeded durable daily cap: %+v", alert)
	}
}

func TestDiskWriteAlertStateReadFailureFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		make func(string) error
	}{
		{name: "corrupt", make: func(path string) error { return os.WriteFile(path, []byte("{"), 0o600) }},
		{name: "unreadable", make: func(path string) error { return os.Mkdir(path, 0o700) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewDiskWriteStore(dir)
			if err := test.make(store.alertStatePath()); err != nil {
				t.Fatal(err)
			}
			policy := defaultDiskWritePolicy()
			policy.NotificationsEnabled = true
			observer := NewDiskWriteObserver(dir, policy)
			if !observer.alertStateInvalid {
				t.Fatal("alert state read failure did not invalidate notification deduplication")
			}
			if alert := observer.alertLocked(DiskWriteSummary{
				CapturedAt: time.Now(),
				State:      DiskWriteStateHigh,
			}); alert != nil {
				t.Fatalf("invalid alert ledger emitted a duplicate-risk notification: %+v", alert)
			}
		})
	}
}

func TestDiskWriteWriterAvailabilityAndTruncationStayHonest(t *testing.T) {
	observer := NewDiskWriteObserver(t.TempDir(), defaultDiskWritePolicy())
	observer.store = nil
	point := diskWriterPoint{At: time.Now(), Duration: 15 * time.Second}
	for index := 0; index < diskWriteMaxWriters+5; index++ {
		point.Deltas = append(point.Deltas, diskProcessDelta{
			Key:        diskProcessKey{PID: index + 10, StartID: uint64(index + 1)},
			Executable: "writer-" + strings.Repeat("x", index), Bytes: uint64(index + 1),
		})
	}
	observer.writerPoints = []diskWriterPoint{point}
	writers, total, available := observer.aggregateWritersLocked(15 * time.Second)
	report := limitDiskWriteReport(DiskWriteReport{Writers: writers, AvailableCount: available}, diskWriteMaxWriters)
	if available != diskWriteMaxWriters+5 || len(report.Writers) != diskWriteMaxWriters || report.ReturnedCount != diskWriteMaxWriters || !report.Truncated {
		t.Fatalf("writer bounds are dishonest: available=%d report=%+v", available, report)
	}
	if total != 325 {
		t.Fatalf("writer total=%d want=325", total)
	}
}

func TestDiskWriteTelemetryNeverPersistsWriterIdentity(t *testing.T) {
	store := NewTelemetryStore(t.TempDir())
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	summary := disabledDiskWriteSummary(now, true)
	event := TelemetryEvent{Timestamp: now, Event: "state_transition", Snapshot: &Snapshot{
		SchemaVersion: SchemaVersion, Timestamp: now, DiskWrite: &summary,
		DiskWriteWriters: []DiskWriter{{DiskWriterSummary: DiskWriterSummary{Executable: "sqlite3"}, PID: 4242, ProcessStartID: 99}},
	}}
	if err := store.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(store.dayPath(now))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "4242") || strings.Contains(text, "disk_write_writers") {
		t.Fatalf("writer identity leaked to telemetry: %s", text)
	}
}

func TestMonitorCheckpointPersistsSummaryWithoutDuplicateWriterList(t *testing.T) {
	observer := NewDiskWriteObserver(t.TempDir(), defaultDiskWritePolicy())
	observer.store = nil
	top := DiskWriterSummary{Executable: "sqlite3", Category: "database", WindowBytes: 4 << 20}
	observer.lastReport = DiskWriteReport{
		Summary: DiskWriteSummary{
			SchemaVersion: DiskWriteSummarySchemaVersion, ModelVersion: diskWriteModelVersion,
			CapturedAt: time.Now().UTC(), State: DiskWriteStateLearning, TopWriter: &top,
			WriterAvailableCount: 20,
		},
		Writers:        []DiskWriter{{DiskWriterSummary: top, PID: 4242, ProcessStartID: 99}},
		AvailableCount: 20,
	}
	monitor := &Monitor{DiskObserver: observer}
	snapshot := Snapshot{CoordinatedWork: CoordinatedWorkSnapshot{}}
	monitor.attachDiskWrite(&snapshot)
	if snapshot.DiskWrite == nil || snapshot.DiskWrite.TopWriter == nil || snapshot.DiskWrite.TopWriter.Executable != "sqlite3" {
		t.Fatalf("compact disk summary missing: %+v", snapshot.DiskWrite)
	}
	if len(snapshot.DiskWriteWriters) != 0 {
		t.Fatalf("latest checkpoint duplicated writer rows: %+v", snapshot.DiskWriteWriters)
	}
}

func TestDiskWriteHistoryIsBoundedAndReadsCompactPoints(t *testing.T) {
	store := NewDiskWriteStore(t.TempDir())
	hour := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	histogram := diskHistogram{}
	for index := 0; index < 240; index++ {
		histogram.add(uint64(index+1)<<20, hour.Add(time.Duration(index)*15*time.Second))
	}
	record := diskWriteHistoryRecord{
		SchemaVersion: 1, ModelVersion: diskWriteModelVersion, Hour: hour,
		State: DiskWriteStateNormal, BytesWritten: 1 << 30, SampleCount: 240, BaselineP99Bytes: 2 << 30,
		Contexts: map[string]diskHistogramRecord{"uncoordinated": histogram.record()},
	}
	for index := 0; index < 32; index++ {
		record.Executables = append(record.Executables, diskExecutableHistory{Executable: strings.Repeat("x", 40) + string(rune('a'+index%26)), TotalBytes: uint64(index + 1), Histogram: histogram.record()})
	}
	if err := store.appendRecord(record); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.dayPath(hour))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxDiskWriteHistoryRecordBytes+1 {
		t.Fatalf("history row size=%d exceeds ceiling", info.Size())
	}
	points, available, err := store.ReadHistory(20, hour.Add(-time.Hour))
	if err != nil || available != 1 || len(points) != 1 || points[0].SampleCount != 240 || points[0].BytesWritten != 1<<30 {
		t.Fatalf("compact history points=%+v available=%d err=%v", points, available, err)
	}
}

func TestDiskWriteHistoryUsesLatestSameHourStateAndAggregatesTraffic(t *testing.T) {
	store := NewDiskWriteStore(t.TempDir())
	hour := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	records := []diskWriteHistoryRecord{
		{
			SchemaVersion: 1, ModelVersion: diskWriteModelVersion, Hour: hour,
			LastSampleAt: hour.Add(10 * time.Minute), State: DiskWriteStateNormal,
			BytesWritten: 10, SampleCount: 1, BaselineP99Bytes: 100,
			Contexts: map[string]diskHistogramRecord{},
		},
		{
			SchemaVersion: 1, ModelVersion: diskWriteModelVersion, Hour: hour,
			LastSampleAt: hour.Add(40 * time.Minute), State: DiskWriteStateHigh,
			BytesWritten: 20, UnscoredGapBytes: 3, SampleCount: 2, BaselineP99Bytes: 200,
			Contexts: map[string]diskHistogramRecord{},
		},
	}
	for _, record := range records {
		if err := store.appendRecord(record); err != nil {
			t.Fatal(err)
		}
	}
	points, available, err := store.ReadHistory(20, hour.Add(-time.Hour))
	if err != nil || available != 1 || len(points) != 1 {
		t.Fatalf("same-hour history points=%+v available=%d err=%v", points, available, err)
	}
	point := points[0]
	if point.BytesWritten != 30 || point.UnscoredGapBytes != 3 || point.SampleCount != 3 ||
		point.State != DiskWriteStateHigh || point.BaselineP99Bytes != 200 {
		t.Fatalf("same-hour aggregation did not use latest model fields: %+v", point)
	}
}

func TestDiskWriteCheckpointOverwritesAndResumesWithoutDuplicateHistory(t *testing.T) {
	dir := t.TempDir()
	store := NewDiskWriteStore(dir)
	// Restore applies BaselineRetentionDays against time.Now. A hardcoded
	// 2026-07-22 hour ages out of the 14-day window and looks like "did not resume".
	hour := time.Now().UTC().Truncate(time.Hour)
	first := diskWriteHistoryRecord{
		SchemaVersion: 1, ModelVersion: diskWriteModelVersion, Hour: hour,
		LastSampleAt: hour.Add(10 * time.Minute), State: DiskWriteStateLearning,
		BytesWritten: 10, SampleCount: 1, DeviceIdentity: "disk0", DeviceBytes: 1_000,
		Contexts: map[string]diskHistogramRecord{},
	}
	second := first
	second.LastSampleAt = hour.Add(20 * time.Minute)
	second.State = DiskWriteStateNormal
	second.BytesWritten = 25
	second.SampleCount = 2
	second.DeviceBytes = 1_015
	if err := store.writeCheckpoint(first); err != nil {
		t.Fatal(err)
	}
	if err := store.writeCheckpoint(second); err != nil {
		t.Fatal(err)
	}

	points, available, err := store.ReadHistory(20, hour.Add(-time.Hour))
	if err != nil || available != 1 || len(points) != 1 || points[0].BytesWritten != 25 || points[0].SampleCount != 2 {
		t.Fatalf("checkpoint overwrite history=%+v available=%d err=%v", points, available, err)
	}
	if _, err := os.Stat(store.dayPath(hour)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint unexpectedly appended completed history: %v", err)
	}

	restored := NewDiskWriteObserver(dir, defaultDiskWritePolicy())
	if restored.currentHour == nil || !restored.currentHour.Hour.Equal(hour) ||
		restored.previousAt != second.LastSampleAt || restored.previousDevice.BytesWritten != second.DeviceBytes {
		t.Fatalf("checkpoint did not resume observer: hour=%+v at=%s device=%+v", restored.currentHour, restored.previousAt, restored.previousDevice)
	}
	restored.mu.Lock()
	restored.flushHourLocked()
	restored.mu.Unlock()
	if _, err := os.Stat(store.checkpointPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed flush did not remove checkpoint: %v", err)
	}
	body, err := os.ReadFile(store.dayPath(hour))
	if err != nil {
		t.Fatal(err)
	}
	if rows := strings.Count(strings.TrimSpace(string(body)), "\n") + 1; rows != 1 {
		t.Fatalf("completed history rows=%d want=1\n%s", rows, body)
	}
	points, available, err = store.ReadHistory(20, hour.Add(-time.Hour))
	if err != nil || available != 1 || len(points) != 1 || points[0].BytesWritten != 25 || points[0].SampleCount != 2 {
		t.Fatalf("flushed history duplicated checkpoint=%+v available=%d err=%v", points, available, err)
	}
}

func TestDiskWriteRestartRestoresAnomalousHysteresis(t *testing.T) {
	dir := t.TempDir()
	store := NewDiskWriteStore(dir)
	hour := time.Now().UTC().Truncate(time.Hour)
	if err := store.writeCheckpoint(diskWriteHistoryRecord{
		SchemaVersion: 1, ModelVersion: diskWriteModelVersion, Hour: hour,
		LastSampleAt: hour.Add(30 * time.Minute), State: DiskWriteStateHigh,
		BytesWritten: 100, SampleCount: 2, DeviceIdentity: "disk0", DeviceBytes: 1_000,
		Contexts: map[string]diskHistogramRecord{},
	}); err != nil {
		t.Fatal(err)
	}
	observer := NewDiskWriteObserver(dir, defaultDiskWritePolicy())
	if observer.lastState != DiskWriteStateHigh {
		t.Fatalf("restored state=%s want high", observer.lastState)
	}
	state, accepted := observer.classifyLocked(
		hour.Add(31*time.Minute),
		DiskWriteConfidenceConfident,
		4,
		100,
		nil,
		0,
	)
	if state != DiskWriteStateHigh || accepted {
		t.Fatalf("restart recovery plateau state=%s accepted=%v; want high,false", state, accepted)
	}
}

func TestDiskWriteRunCancellationPersistsCurrentCheckpoint(t *testing.T) {
	dir := t.TempDir()
	observer := NewDiskWriteObserver(dir, defaultDiskWritePolicy())
	observer.deviceSource = func(context.Context) (diskDeviceCounter, error) {
		return diskDeviceCounter{
			BytesWritten: 1_000,
			DeviceCount:  1,
			Identity:     "disk0",
			Source:       "test",
		}, nil
	}
	observer.processSource = func(context.Context) (diskProcessSnapshot, error) {
		return diskProcessSnapshot{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := observer.Run(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := NewDiskWriteStore(dir).loadCheckpoint(time.Time{})
	if err != nil || !found || checkpoint.DeviceIdentity != "disk0" || checkpoint.DeviceBytes != 1_000 {
		t.Fatalf("clean cancellation checkpoint=%+v found=%v err=%v", checkpoint, found, err)
	}
	if _, err := os.Stat(NewDiskWriteStore(dir).dayPath(checkpoint.Hour)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean cancellation appended completed history: %v", err)
	}
}

func TestDiskWriteHistoryAggregatesHourlyTrafficAndRestartCheckpoints(t *testing.T) {
	dir := t.TempDir()
	policy := defaultDiskWritePolicy()
	observer := NewDiskWriteObserver(dir, policy)
	start := time.Now().UTC().Truncate(time.Hour).Add(-30 * time.Second)
	deviceBytes := uint64(1_000)
	observer.deviceSource = func(context.Context) (diskDeviceCounter, error) {
		value := diskDeviceCounter{BytesWritten: deviceBytes, DeviceCount: 1, Identity: "disk0", Source: "test"}
		deviceBytes += 1_500
		return value, nil
	}
	observer.processSource = func(context.Context) (diskProcessSnapshot, error) { return diskProcessSnapshot{}, nil }
	observer.Sample(context.Background(), start)
	observer.Sample(context.Background(), start.Add(15*time.Second))
	observer.Sample(context.Background(), start.Add(30*time.Second))
	observer.mu.Lock()
	observer.flushHourLocked()
	observer.mu.Unlock()

	points, available, err := NewDiskWriteStore(dir).ReadHistory(20, start.Add(-time.Hour))
	if err != nil || available != 2 || len(points) != 2 {
		t.Fatalf("history points=%+v available=%d err=%v", points, available, err)
	}
	if points[0].BytesWritten != 1_500 || points[0].SampleCount != 1 || points[1].BytesWritten != 1_500 || points[1].SampleCount != 1 {
		t.Fatalf("hourly traffic was not counted exactly once: %+v", points)
	}

	restored := NewDiskWriteObserver(dir, policy)
	if restored.previousAt.IsZero() || restored.previousDevice.Identity != "disk0" || restored.previousDevice.BytesWritten != 4_000 {
		t.Fatalf("restart checkpoint not restored: at=%s device=%+v", restored.previousAt, restored.previousDevice)
	}
}

func TestDiskWriteRestoreCarriesRollingTotalsAndGapEvidence(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	record := diskWriteHistoryRecord{
		SchemaVersion: 1, ModelVersion: diskWriteModelVersion, Hour: now.Truncate(time.Hour),
		State: DiskWriteStateLearning, BytesWritten: 2_000, UnscoredGapBytes: 300, SampleCount: 2,
		Contexts: map[string]diskHistogramRecord{}, LastSampleAt: now.Add(-15 * time.Second),
		DeviceIdentity: "disk0", DeviceBytes: 50_000, DeviceCount: 1, DeviceSource: "test",
	}
	if err := NewDiskWriteStore(dir).appendRecord(record); err != nil {
		t.Fatal(err)
	}
	observer := NewDiskWriteObserver(dir, defaultDiskWritePolicy())
	observer.mu.RLock()
	total := observer.windowBytesLocked(now.Add(-diskWriteHistoryWindow), false)
	gap := observer.windowGapBytesLocked(now.Add(-diskWriteHistoryWindow))
	baselineSamples := observer.baselines["uncoordinated"]
	observer.mu.RUnlock()
	if total != 2_300 || gap != 300 {
		t.Fatalf("restored total=%d gap=%d", total, gap)
	}
	if baselineSamples != nil && baselineSamples.Count != 0 {
		t.Fatalf("hourly traffic was reconstructed as a fake baseline sample: %+v", baselineSamples)
	}
}

func TestDiskWriteHistoryPrunesOnlyExpiredOwnedShards(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.Local)
	store := NewDiskWriteStore(dir)
	store.Now = func() time.Time { return now }
	oldPath := store.dayPath(now.AddDate(0, 0, -3))
	currentPath := store.dayPath(now.AddDate(0, 0, -1))
	for _, path := range []string{oldPath, currentPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(dir, "keep-me.jsonl")
	if err := os.WriteFile(unrelated, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired owned shard still exists: %v", err)
	}
	for _, path := range []string{currentPath, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("prune touched retained path %s: %v", path, err)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
