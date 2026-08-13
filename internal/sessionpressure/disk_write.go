package sessionpressure

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DiskWriteSummarySchemaVersion = 1
	diskWriteModelVersion         = DiskWriteProfileQuietAdaptiveV1
	diskWriteWindow               = 15 * time.Minute
	diskWriteHistoryWindow        = 24 * time.Hour
	diskWriteMinimumWindow        = 14 * time.Minute
	diskWriteLearningSamples      = 480
	diskWriteConfidentSamples     = 6720
	diskWriteLearningAge          = 2 * time.Hour
	diskWriteConfidentAge         = 7 * 24 * time.Hour
	diskWriteUnusualRatio         = 6.0
	diskWriteHighRatio            = 12.0
	diskWriteRecoveryRatio        = 2.0
	diskWriteUnusualDuration      = 5 * time.Minute
	diskWriteHighDuration         = 10 * time.Minute
	diskWriteRecoveryDuration     = 30 * time.Minute
	diskWriteTransitionInterval   = 15 * time.Minute
	diskWriteAlertLimitPerDay     = 4
	diskWriteMaxWriters           = 20
	diskWriteMaxExecutableModels  = 32
)

// DiskWriteState is diagnostic and never contributes to host pressure level,
// admission, cleanup, or process-relief authority.
type DiskWriteState string

const (
	DiskWriteStateDisabled    DiskWriteState = "disabled"
	DiskWriteStateUnavailable DiskWriteState = "unavailable"
	DiskWriteStateLearning    DiskWriteState = "learning"
	DiskWriteStateNormal      DiskWriteState = "normal"
	DiskWriteStateUnusual     DiskWriteState = "unusual"
	DiskWriteStateHigh        DiskWriteState = "high"
)

type DiskWriteConfidence string

const (
	DiskWriteConfidenceNone        DiskWriteConfidence = "none"
	DiskWriteConfidenceLearning    DiskWriteConfidence = "learning"
	DiskWriteConfidenceProvisional DiskWriteConfidence = "provisional"
	DiskWriteConfidenceConfident   DiskWriteConfidence = "confident"
)

type DiskWriterSummary struct {
	Executable        string  `json:"executable"`
	Category          string  `json:"category"`
	ProcessCount      int     `json:"process_count"`
	AgentProcessCount int     `json:"agent_process_count"`
	WindowBytes       uint64  `json:"window_bytes"`
	BytesPerSecond    float64 `json:"bytes_per_second"`
	BaselineRatio     float64 `json:"baseline_ratio,omitempty"`
}

// DiskWriter is a bounded, basename-only attribution candidate. PID and start
// identity are emitted only by live/full commands and latest.json; telemetry
// strips the complete writer list before persistence.
type DiskWriter struct {
	DiskWriterSummary
	PID            int    `json:"pid,omitempty"`
	ProcessStartID uint64 `json:"process_start_id,omitempty"`
	OwnerKind      string `json:"owner_kind,omitempty"`
	WorkClass      string `json:"work_class,omitempty"`
}

type DiskWriteSummary struct {
	SchemaVersion            int                 `json:"schema_version"`
	ModelVersion             string              `json:"model_version"`
	CapturedAt               time.Time           `json:"captured_at"`
	State                    DiskWriteState      `json:"state"`
	Confidence               DiskWriteConfidence `json:"confidence"`
	Source                   string              `json:"source"`
	DeviceScope              string              `json:"device_scope"`
	AttributionScope         string              `json:"attribution_scope"`
	Context                  string              `json:"context"`
	MeasurementWindowSeconds float64             `json:"measurement_window_seconds,omitempty"`
	CurrentBytesPerSecond    float64             `json:"current_bytes_per_second,omitempty"`
	Window15mBytes           uint64              `json:"window_15m_bytes,omitempty"`
	Bytes24h                 uint64              `json:"bytes_24h,omitempty"`
	UnscoredGapBytes         uint64              `json:"unscored_gap_bytes,omitempty"`
	BaselineP99Bytes15m      uint64              `json:"baseline_p99_bytes_15m,omitempty"`
	BaselineRatio            float64             `json:"baseline_ratio,omitempty"`
	BaselineSamples          uint64              `json:"baseline_samples,omitempty"`
	BaselineAgeSeconds       float64             `json:"baseline_age_seconds,omitempty"`
	DeviceCount              int                 `json:"device_count,omitempty"`
	TotalPIDCount            int                 `json:"total_pid_count,omitempty"`
	AccessiblePIDCount       int                 `json:"accessible_pid_count,omitempty"`
	AttributionAvailable     bool                `json:"attribution_available"`
	WriterAvailableCount     int                 `json:"writer_available_count,omitempty"`
	TopWriter                *DiskWriterSummary  `json:"top_writer,omitempty"`
	Reasons                  []string            `json:"reason_codes,omitempty"`
}

type DiskWriteReport struct {
	Summary        DiskWriteSummary `json:"summary"`
	Writers        []DiskWriter     `json:"writers"`
	AvailableCount int              `json:"available_count"`
	ReturnedCount  int              `json:"returned_count"`
	Truncated      bool             `json:"truncated"`
}

type DiskWriteTransition struct {
	SchemaVersion int                 `json:"schema_version"`
	Timestamp     time.Time           `json:"timestamp"`
	From          DiskWriteState      `json:"from"`
	To            DiskWriteState      `json:"to"`
	Confidence    DiskWriteConfidence `json:"confidence"`
	Ratio         float64             `json:"ratio,omitempty"`
	WindowBytes   uint64              `json:"window_bytes,omitempty"`
	Context       string              `json:"context,omitempty"`
}

type DiskWriteAlert struct {
	Timestamp   time.Time
	State       DiskWriteState
	Ratio       float64
	WindowBytes uint64
	TopWriter   string
}

type diskDeviceCounter struct {
	BytesWritten uint64
	DeviceCount  int
	Identity     string
	Source       string
}

type diskProcessCounter struct {
	PID          int
	StartID      uint64
	Executable   string
	BytesWritten uint64
	AgentOwned   bool
}

type diskProcessSnapshot struct {
	Counters        []diskProcessCounter
	TotalPIDCount   int
	AccessibleCount int
}

type diskProcessKey struct {
	PID     int
	StartID uint64
}

type diskProcessDelta struct {
	Key        diskProcessKey
	Executable string
	Bytes      uint64
	AgentOwned bool
}

type diskVolumePoint struct {
	At     time.Time
	Bytes  uint64
	Scored bool
	Gap    bool
}

type diskWriterPoint struct {
	At       time.Time
	Duration time.Duration
	Deltas   []diskProcessDelta
}

type diskOwnership struct {
	Executable string
	AgentOwned bool
}

type diskHistogram struct {
	Zero    uint64
	Buckets [64]uint64
	Count   uint64
	Sum     uint64
	FirstAt time.Time
	LastAt  time.Time
}

func (histogram *diskHistogram) add(value uint64, at time.Time) {
	if value == 0 {
		histogram.Zero++
	} else {
		index := bits.Len64(value) - 1
		histogram.Buckets[index]++
	}
	histogram.Count++
	if histogram.Sum <= ^uint64(0)-value {
		histogram.Sum += value
	} else {
		histogram.Sum = ^uint64(0)
	}
	if histogram.FirstAt.IsZero() || at.Before(histogram.FirstAt) {
		histogram.FirstAt = at
	}
	if at.After(histogram.LastAt) {
		histogram.LastAt = at
	}
}

func (histogram *diskHistogram) merge(record diskHistogramRecord) {
	histogram.Zero += record.Zero
	for _, bucket := range record.Buckets {
		if bucket.Index >= 0 && bucket.Index < len(histogram.Buckets) {
			histogram.Buckets[bucket.Index] += bucket.Count
		}
	}
	histogram.Count += record.Count
	if histogram.Sum <= ^uint64(0)-record.Sum {
		histogram.Sum += record.Sum
	} else {
		histogram.Sum = ^uint64(0)
	}
	if histogram.FirstAt.IsZero() || (!record.FirstAt.IsZero() && record.FirstAt.Before(histogram.FirstAt)) {
		histogram.FirstAt = record.FirstAt
	}
	if record.LastAt.After(histogram.LastAt) {
		histogram.LastAt = record.LastAt
	}
}

func (histogram diskHistogram) p99() uint64 {
	if histogram.Count == 0 {
		return 0
	}
	want := uint64(math.Ceil(float64(histogram.Count) * 0.99))
	seen := histogram.Zero
	if seen >= want {
		return 0
	}
	for index, count := range histogram.Buckets {
		seen += count
		if seen < want {
			continue
		}
		if index == 63 {
			return ^uint64(0)
		}
		return (uint64(1) << (index + 1)) - 1
	}
	return 0
}

type DiskWriteObserver struct {
	mu sync.RWMutex

	policy        DiskWritePolicy
	store         *DiskWriteStore
	deviceSource  func(context.Context) (diskDeviceCounter, error)
	processSource func(context.Context) (diskProcessSnapshot, error)
	now           func() time.Time
	allowShort    bool

	previousDevice    diskDeviceCounter
	previousProcesses map[diskProcessKey]diskProcessCounter
	previousAt        time.Time
	volumePoints      []diskVolumePoint
	writerPoints      []diskWriterPoint
	baselines         map[string]*diskHistogram
	executableModels  map[string]*diskHistogram
	executableBytes   map[string]uint64
	ownership         map[diskProcessKey]diskOwnership
	context           string
	currentHour       *diskWriteHistoryRecord
	lastReport        DiskWriteReport
	lastState         DiskWriteState
	unusualSince      time.Time
	highSince         time.Time
	recoverySince     time.Time
	lastTransition    time.Time
	incidentOpen      bool
	alertDay          string
	alertsToday       int
	alertStateInvalid bool
	dominance         []string
}

func NewDiskWriteObserver(dir string, policy DiskWritePolicy) *DiskWriteObserver {
	observer := &DiskWriteObserver{
		policy: policy, store: NewDiskWriteStore(dir), deviceSource: nativeDiskDeviceCounter,
		processSource: nativeDiskProcessCounters, now: time.Now,
		previousProcesses: make(map[diskProcessKey]diskProcessCounter), baselines: make(map[string]*diskHistogram),
		executableModels: make(map[string]*diskHistogram), executableBytes: make(map[string]uint64),
		ownership: make(map[diskProcessKey]diskOwnership), context: "uncoordinated", lastState: DiskWriteStateLearning,
	}
	observer.restore()
	return observer
}

func (observer *DiskWriteObserver) restore() {
	if observer == nil || observer.store == nil {
		return
	}
	if alertState, found, alertErr := observer.store.loadAlertState(); alertErr != nil {
		// Notification deduplication is safety state. Any read failure must fail
		// closed so a permissions or I/O error cannot reset the daily cap.
		observer.alertStateInvalid = true
	} else if found {
		observer.alertDay = alertState.Day
		observer.alertsToday = alertState.Alerts
		observer.incidentOpen = alertState.IncidentOpen
		if observer.incidentOpen {
			observer.lastState = DiskWriteStateHigh
		}
	}
	if observer.policy.BaselineRetentionDays <= 0 {
		return
	}
	since := observer.now().UTC().AddDate(0, 0, -observer.policy.BaselineRetentionDays)
	records, checkpoint, err := observer.store.loadRecordsWithCheckpoint(since)
	if err != nil {
		return
	}
	var newest *diskWriteHistoryRecord
	for index := range records {
		record := records[index]
		for contextName, saved := range record.Contexts {
			histogram := observer.baselines[contextName]
			if histogram == nil {
				histogram = &diskHistogram{}
				observer.baselines[contextName] = histogram
			}
			histogram.merge(saved)
		}
		for _, executable := range record.Executables {
			histogram := observer.executableModels[executable.Executable]
			if histogram == nil {
				histogram = &diskHistogram{}
				observer.executableModels[executable.Executable] = histogram
			}
			histogram.merge(executable.Histogram)
			observer.executableBytes[executable.Executable] = saturatingAddUint64(observer.executableBytes[executable.Executable], executable.TotalBytes)
		}
		pointAt := record.LastSampleAt.UTC()
		if pointAt.IsZero() {
			pointAt = record.Hour.UTC()
		}
		if record.BytesWritten > 0 {
			// Restored hourly traffic contributes to the rolling 24-hour
			// total, but never reconstructs a fake 15-minute sample.
			observer.volumePoints = append(observer.volumePoints, diskVolumePoint{At: pointAt, Bytes: record.BytesWritten})
		}
		if record.UnscoredGapBytes > 0 {
			observer.volumePoints = append(observer.volumePoints, diskVolumePoint{At: pointAt, Bytes: record.UnscoredGapBytes, Gap: true})
		}
		if !record.LastSampleAt.IsZero() && record.DeviceIdentity != "" && (newest == nil || record.LastSampleAt.After(newest.LastSampleAt)) {
			copy := record
			newest = &copy
		}
	}
	if newest != nil {
		observer.previousAt = newest.LastSampleAt.UTC()
		observer.previousDevice = diskDeviceCounter{
			BytesWritten: newest.DeviceBytes,
			DeviceCount:  newest.DeviceCount,
			Identity:     newest.DeviceIdentity,
			Source:       newest.DeviceSource,
		}
	}
	if checkpoint != nil {
		current := *checkpoint
		current.Contexts = make(map[string]diskHistogramRecord, len(checkpoint.Contexts))
		for contextName, histogram := range checkpoint.Contexts {
			current.Contexts[contextName] = histogram
		}
		current.executableHistograms = make(map[string]*diskHistogram, len(checkpoint.Executables))
		for _, executable := range checkpoint.Executables {
			histogram := diskHistogramFromRecord(executable.Histogram)
			current.executableHistograms[executable.Executable] = &histogram
		}
		observer.currentHour = &current
		if current.State == DiskWriteStateHigh || current.State == DiskWriteStateUnusual {
			observer.lastState = current.State
		}
	}
	observer.trimExecutableModels()
}

func (observer *DiskWriteObserver) SetProcessOwnership(processes []Process) {
	if observer == nil {
		return
	}
	owners := make(map[diskProcessKey]diskOwnership, len(processes))
	for _, process := range processes {
		if process.PID <= 0 || process.CPUStartID == 0 {
			continue
		}
		owners[diskProcessKey{PID: process.PID, StartID: process.CPUStartID}] = diskOwnership{
			Executable: privacySafeExecutable(process.Executable), AgentOwned: process.Agent != "",
		}
	}
	observer.mu.Lock()
	observer.ownership = owners
	observer.mu.Unlock()
}

func (observer *DiskWriteObserver) SetContext(value string) {
	if observer == nil {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = "uncoordinated"
	}
	observer.mu.Lock()
	if observer.context != value {
		observer.context = value
		observer.unusualSince = time.Time{}
		observer.highSince = time.Time{}
		observer.recoverySince = time.Time{}
		observer.dominance = nil
	}
	observer.mu.Unlock()
}

func diskWriteContext(work CoordinatedWorkSnapshot) string {
	classes := make([]string, 0, len(work.ByClass))
	for _, class := range work.ByClass {
		if class.LeaseCount > 0 {
			classes = append(classes, string(class.Class))
		}
	}
	if len(classes) == 0 {
		return "uncoordinated"
	}
	if len(classes) > 1 {
		return "mixed"
	}
	return classes[0]
}

func (observer *DiskWriteObserver) Latest(limit int) DiskWriteReport {
	if observer == nil {
		return DiskWriteReport{Summary: unavailableDiskWriteSummary(time.Now().UTC(), "observer_unavailable")}
	}
	observer.mu.RLock()
	report := cloneDiskWriteReport(observer.lastReport)
	observer.mu.RUnlock()
	if report.Summary.SchemaVersion == 0 {
		report.Summary = disabledDiskWriteSummary(observer.now().UTC(), observer.policy.Enabled)
	}
	return limitDiskWriteReport(report, limit)
}

func cloneDiskWriteReport(report DiskWriteReport) DiskWriteReport {
	report.Writers = append([]DiskWriter(nil), report.Writers...)
	report.Summary.Reasons = append([]string(nil), report.Summary.Reasons...)
	if report.Summary.TopWriter != nil {
		top := *report.Summary.TopWriter
		report.Summary.TopWriter = &top
	}
	return report
}

func limitDiskWriteReport(report DiskWriteReport, limit int) DiskWriteReport {
	if limit <= 0 {
		limit = 5
	}
	if limit > diskWriteMaxWriters {
		limit = diskWriteMaxWriters
	}
	report.AvailableCount = max(report.AvailableCount, len(report.Writers))
	if len(report.Writers) > limit {
		report.Writers = append([]DiskWriter(nil), report.Writers[:limit]...)
	}
	report.ReturnedCount = len(report.Writers)
	report.Truncated = report.AvailableCount > report.ReturnedCount
	return report
}

// MergeLiveDiskWriteReport combines a fresh, bounded rate/attribution sample
// with the resident-owned adaptive model. A one-second observer can measure
// current bytes and writers, but it can never independently satisfy the
// fourteen-minute rolling-window gate; state, confidence, rolling totals, and
// baseline evidence therefore remain owned by the healthy resident.
func MergeLiveDiskWriteReport(live DiskWriteReport, resident DiskWriteSummary) DiskWriteReport {
	summary := live.Summary
	summary.ModelVersion = resident.ModelVersion
	summary.State = resident.State
	summary.Confidence = resident.Confidence
	summary.Context = resident.Context
	summary.Window15mBytes = resident.Window15mBytes
	summary.Bytes24h = resident.Bytes24h
	summary.UnscoredGapBytes = resident.UnscoredGapBytes
	summary.BaselineP99Bytes15m = resident.BaselineP99Bytes15m
	summary.BaselineRatio = resident.BaselineRatio
	summary.BaselineSamples = resident.BaselineSamples
	summary.BaselineAgeSeconds = resident.BaselineAgeSeconds
	summary.Reasons = append([]string(nil), resident.Reasons...)
	for _, reason := range live.Summary.Reasons {
		switch reason {
		case "process_attribution_unavailable", "process_attribution_partial":
			if !diskWriteReasonsContain(summary.Reasons, reason) {
				summary.Reasons = append(summary.Reasons, reason)
			}
		}
	}
	live.Summary = summary
	return live
}

func diskWriteReasonsContain(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func disabledDiskWriteSummary(at time.Time, enabled bool) DiskWriteSummary {
	state := DiskWriteStateDisabled
	reasons := []string{"monitoring_disabled"}
	if enabled {
		state = DiskWriteStateLearning
		reasons = []string{"awaiting_first_sample"}
	}
	return DiskWriteSummary{
		SchemaVersion: DiskWriteSummarySchemaVersion, ModelVersion: diskWriteModelVersion,
		CapturedAt: at.UTC(), State: state, Confidence: DiskWriteConfidenceNone,
		Source: "iokit+libproc", DeviceScope: "internal_ssd", AttributionScope: "all_disk_io_best_effort",
		Context: "uncoordinated", Reasons: reasons,
	}
}

func unavailableDiskWriteSummary(at time.Time, reason string) DiskWriteSummary {
	summary := disabledDiskWriteSummary(at, true)
	summary.State = DiskWriteStateUnavailable
	summary.Reasons = []string{reason}
	return summary
}

// Sample reads cumulative native counters and advances the in-memory model.
// The call never persists a per-sample record.
func (observer *DiskWriteObserver) Sample(ctx context.Context, at time.Time) (DiskWriteReport, *DiskWriteTransition, *DiskWriteAlert) {
	if observer == nil {
		report := DiskWriteReport{Summary: unavailableDiskWriteSummary(at, "observer_unavailable")}
		return report, nil, nil
	}
	if at.IsZero() {
		at = observer.now().UTC()
	} else {
		at = at.UTC()
	}
	if !observer.policy.Enabled {
		report := DiskWriteReport{Summary: disabledDiskWriteSummary(at, false), Writers: []DiskWriter{}}
		observer.mu.Lock()
		observer.lastReport = report
		observer.mu.Unlock()
		return report, nil, nil
	}
	device, deviceErr := observer.deviceSource(ctx)
	processes, processErr := observer.processSource(ctx)

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if deviceErr != nil {
		summary := unavailableDiskWriteSummary(at, "device_counter_unavailable")
		summary.Reasons = append(summary.Reasons, boundedText(deviceErr.Error(), 96))
		if processErr != nil {
			summary.Reasons = append(summary.Reasons, "process_attribution_unavailable")
		}
		report := DiskWriteReport{Summary: summary, Writers: []DiskWriter{}}
		transition := observer.transitionLocked(report.Summary)
		observer.lastReport = report
		return cloneDiskWriteReport(report), transition, nil
	}

	elapsed := at.Sub(observer.previousAt)
	hasPrevious := !observer.previousAt.IsZero() && device.Identity == observer.previousDevice.Identity && device.BytesWritten >= observer.previousDevice.BytesWritten
	maxInterval := time.Duration(max(45, observer.policy.SampleIntervalSeconds*3)) * time.Second
	validWindow := hasPrevious && elapsed > 0 && elapsed <= maxInterval
	if !observer.allowShort {
		validWindow = validWindow && elapsed >= time.Duration(max(1, observer.policy.SampleIntervalSeconds/3))*time.Second
	}
	deviceDelta := uint64(0)
	if hasPrevious {
		deviceDelta = device.BytesWritten - observer.previousDevice.BytesWritten
		observer.volumePoints = append(observer.volumePoints, diskVolumePoint{At: at, Bytes: deviceDelta, Scored: validWindow, Gap: !validWindow})
	}

	processDeltas := make([]diskProcessDelta, 0, len(processes.Counters))
	currentProcesses := make(map[diskProcessKey]diskProcessCounter, len(processes.Counters))
	for _, process := range processes.Counters {
		key := diskProcessKey{PID: process.PID, StartID: process.StartID}
		if owner, ok := observer.ownership[key]; ok {
			if owner.Executable != "" && owner.Executable != "unknown" {
				process.Executable = owner.Executable
			}
			process.AgentOwned = process.AgentOwned || owner.AgentOwned
		}
		process.Executable = privacySafeExecutable(process.Executable)
		currentProcesses[key] = process
		previous, ok := observer.previousProcesses[key]
		if !ok || process.BytesWritten < previous.BytesWritten || !validWindow {
			continue
		}
		delta := process.BytesWritten - previous.BytesWritten
		if delta == 0 {
			continue
		}
		processDeltas = append(processDeltas, diskProcessDelta{Key: key, Executable: process.Executable, Bytes: delta, AgentOwned: process.AgentOwned})
	}
	if hasPrevious {
		observer.writerPoints = append(observer.writerPoints, diskWriterPoint{At: at, Duration: elapsed, Deltas: processDeltas})
	}
	observer.previousAt = at
	observer.previousDevice = device
	observer.previousProcesses = currentProcesses
	observer.prunePointsLocked(at)

	window15 := observer.windowBytesLocked(at.Add(-diskWriteWindow), true)
	bytes24h := observer.windowBytesLocked(at.Add(-diskWriteHistoryWindow), false)
	writers, writerTotal, writerAvailable := observer.aggregateWritersLocked(elapsed)
	contextName := observer.context
	if contextName == "" {
		contextName = "uncoordinated"
	}
	histogram := observer.baselines[contextName]
	if histogram == nil {
		histogram = &diskHistogram{}
		observer.baselines[contextName] = histogram
	}
	p99 := histogram.p99()
	ratio := 0.0
	if p99 > 0 {
		ratio = float64(window15) / float64(p99)
	}
	confidence := diskWriteConfidenceFor(*histogram, at)
	state, accepted := observer.classifyLocked(at, confidence, ratio, p99, writers, writerTotal)
	if !observer.windowCoveredLocked(at) {
		state = DiskWriteStateLearning
		confidence = DiskWriteConfidenceLearning
		accepted = false
	}
	observer.recordHourLocked(at, state, p99, deviceDelta, device, hasPrevious, validWindow)
	if accepted {
		histogram.add(window15, at)
		observer.recordBaselineLocked(at, contextName, window15, writers)
	}

	summary := DiskWriteSummary{
		SchemaVersion: DiskWriteSummarySchemaVersion, ModelVersion: diskWriteModelVersion,
		CapturedAt: at, State: state, Confidence: confidence, Source: device.Source,
		DeviceScope: "internal_ssd", AttributionScope: "all_disk_io_best_effort", Context: contextName,
		Window15mBytes: window15, Bytes24h: bytes24h,
		UnscoredGapBytes: observer.windowGapBytesLocked(at.Add(-diskWriteHistoryWindow)), BaselineP99Bytes15m: p99, BaselineRatio: roundRatio(ratio),
		BaselineSamples: histogram.Count, DeviceCount: device.DeviceCount,
		TotalPIDCount: processes.TotalPIDCount, AccessiblePIDCount: processes.AccessibleCount,
		AttributionAvailable: processErr == nil, WriterAvailableCount: writerAvailable,
	}
	if hasPrevious && elapsed > 0 {
		summary.MeasurementWindowSeconds = elapsed.Seconds()
	}
	if validWindow && elapsed > 0 {
		summary.CurrentBytesPerSecond = float64(deviceDelta) / elapsed.Seconds()
	}
	if !histogram.FirstAt.IsZero() {
		summary.BaselineAgeSeconds = max(0, at.Sub(histogram.FirstAt).Seconds())
	}
	if len(writers) > 0 {
		top := writers[0].DiskWriterSummary
		summary.TopWriter = &top
	}
	switch {
	case !hasPrevious:
		summary.Reasons = append(summary.Reasons, "counter_baseline_initialized")
	case !validWindow:
		summary.Reasons = append(summary.Reasons, "sample_gap_rebased")
	}
	if !observer.windowCoveredLocked(at) {
		summary.Reasons = append(summary.Reasons, "rolling_window_incomplete")
	}
	if p99 == 0 {
		summary.Reasons = append(summary.Reasons, "nonzero_baseline_insufficient")
	}
	if processErr != nil {
		summary.Reasons = append(summary.Reasons, "process_attribution_unavailable")
	}
	if processes.AccessibleCount < processes.TotalPIDCount {
		summary.Reasons = append(summary.Reasons, "process_attribution_partial")
	}
	report := limitDiskWriteReport(DiskWriteReport{Summary: summary, Writers: writers, AvailableCount: writerAvailable}, diskWriteMaxWriters)
	transition := observer.transitionLocked(summary)
	alert := observer.alertLocked(summary)
	observer.lastReport = report
	return cloneDiskWriteReport(report), transition, alert
}

func (observer *DiskWriteObserver) prunePointsLocked(at time.Time) {
	volumeCutoff := at.Add(-diskWriteHistoryWindow)
	first := 0
	for first < len(observer.volumePoints) && observer.volumePoints[first].At.Before(volumeCutoff) {
		first++
	}
	observer.volumePoints = append([]diskVolumePoint(nil), observer.volumePoints[first:]...)
	writerCutoff := at.Add(-diskWriteWindow)
	first = 0
	for first < len(observer.writerPoints) && observer.writerPoints[first].At.Before(writerCutoff) {
		first++
	}
	observer.writerPoints = append([]diskWriterPoint(nil), observer.writerPoints[first:]...)
}

func (observer *DiskWriteObserver) windowBytesLocked(cutoff time.Time, scoredOnly bool) uint64 {
	total := uint64(0)
	for _, point := range observer.volumePoints {
		if point.At.Before(cutoff) || (scoredOnly && !point.Scored) {
			continue
		}
		if total <= ^uint64(0)-point.Bytes {
			total += point.Bytes
		} else {
			return ^uint64(0)
		}
	}
	return total
}

func (observer *DiskWriteObserver) windowGapBytesLocked(cutoff time.Time) uint64 {
	total := uint64(0)
	for _, point := range observer.volumePoints {
		if point.At.Before(cutoff) || !point.Gap {
			continue
		}
		total = saturatingAddUint64(total, point.Bytes)
	}
	return total
}

func (observer *DiskWriteObserver) windowCoveredLocked(at time.Time) bool {
	for _, point := range observer.volumePoints {
		if point.Scored {
			return at.Sub(point.At) >= diskWriteMinimumWindow
		}
	}
	return false
}

func (observer *DiskWriteObserver) aggregateWritersLocked(latestDuration time.Duration) ([]DiskWriter, uint64, int) {
	type aggregate struct {
		bytes, current    uint64
		processes, agents map[diskProcessKey]struct{}
		pid               int
		start             uint64
		largest           uint64
	}
	byExecutable := make(map[string]*aggregate)
	writerTotal := uint64(0)
	for pointIndex, point := range observer.writerPoints {
		latest := pointIndex == len(observer.writerPoints)-1
		for _, delta := range point.Deltas {
			item := byExecutable[delta.Executable]
			if item == nil {
				item = &aggregate{processes: make(map[diskProcessKey]struct{}), agents: make(map[diskProcessKey]struct{})}
				byExecutable[delta.Executable] = item
			}
			item.bytes = saturatingAddUint64(item.bytes, delta.Bytes)
			item.processes[delta.Key] = struct{}{}
			if delta.AgentOwned {
				item.agents[delta.Key] = struct{}{}
			}
			if latest {
				item.current = saturatingAddUint64(item.current, delta.Bytes)
				if delta.Bytes > item.largest {
					item.largest, item.pid, item.start = delta.Bytes, delta.Key.PID, delta.Key.StartID
				}
			}
			writerTotal = saturatingAddUint64(writerTotal, delta.Bytes)
		}
	}
	writers := make([]DiskWriter, 0, len(byExecutable))
	for executable, item := range byExecutable {
		writer := DiskWriter{DiskWriterSummary: DiskWriterSummary{
			Executable: executable, Category: hostConsumerCategory(executable), ProcessCount: len(item.processes),
			AgentProcessCount: len(item.agents), WindowBytes: item.bytes,
		}, PID: item.pid, ProcessStartID: item.start, WorkClass: observer.context}
		if len(item.agents) > 0 {
			writer.OwnerKind = "agent"
		}
		if latestDuration > 0 {
			writer.BytesPerSecond = float64(item.current) / latestDuration.Seconds()
		}
		if model := observer.executableModels[executable]; model != nil {
			if p99 := model.p99(); p99 > 0 {
				writer.BaselineRatio = roundRatio(float64(item.bytes) / float64(p99))
			}
		}
		writers = append(writers, writer)
	}
	sort.Slice(writers, func(i, j int) bool {
		if writers[i].WindowBytes != writers[j].WindowBytes {
			return writers[i].WindowBytes > writers[j].WindowBytes
		}
		return writers[i].Executable < writers[j].Executable
	})
	available := len(writers)
	if available > diskWriteMaxWriters {
		writers = writers[:diskWriteMaxWriters]
	}
	return writers, writerTotal, available
}

func diskWriteConfidenceFor(histogram diskHistogram, at time.Time) DiskWriteConfidence {
	if histogram.Count < diskWriteLearningSamples || histogram.FirstAt.IsZero() || at.Sub(histogram.FirstAt) < diskWriteLearningAge {
		return DiskWriteConfidenceLearning
	}
	if histogram.Count < diskWriteConfidentSamples || at.Sub(histogram.FirstAt) < diskWriteConfidentAge {
		return DiskWriteConfidenceProvisional
	}
	return DiskWriteConfidenceConfident
}

func (observer *DiskWriteObserver) classifyLocked(at time.Time, confidence DiskWriteConfidence, ratio float64, p99 uint64, writers []DiskWriter, writerTotal uint64) (DiskWriteState, bool) {
	if confidence == DiskWriteConfidenceLearning || p99 == 0 {
		observer.unusualSince = time.Time{}
		observer.highSince = time.Time{}
		observer.recoverySince = time.Time{}
		return DiskWriteStateLearning, true
	}
	accepted := ratio < diskWriteUnusualRatio
	if ratio >= diskWriteHighRatio {
		if observer.highSince.IsZero() {
			observer.highSince = at
		}
	} else {
		observer.highSince = time.Time{}
	}
	if ratio >= diskWriteUnusualRatio {
		if observer.unusualSince.IsZero() {
			observer.unusualSince = at
		}
	} else {
		observer.unusualSince = time.Time{}
	}

	dominant := ""
	if len(writers) > 0 && writerTotal > 0 && float64(writers[0].WindowBytes)/float64(writerTotal) >= 0.60 {
		dominant = writers[0].Executable
	}
	observer.dominance = append(observer.dominance, dominant)
	if len(observer.dominance) > 40 {
		observer.dominance = append([]string(nil), observer.dominance[len(observer.dominance)-40:]...)
	}
	dominantConsistent := false
	if dominant != "" && len(observer.dominance) >= 40 {
		matches := 0
		for _, name := range observer.dominance[len(observer.dominance)-40:] {
			if name == dominant {
				matches++
			}
		}
		dominantConsistent = matches >= 32
	}

	high := !observer.highSince.IsZero() && at.Sub(observer.highSince) >= diskWriteHighDuration
	unusual := !observer.unusualSince.IsZero() && at.Sub(observer.unusualSince) >= diskWriteUnusualDuration
	state := DiskWriteStateNormal
	if confidence == DiskWriteConfidenceConfident {
		if high {
			state = DiskWriteStateHigh
		} else if unusual {
			state = DiskWriteStateUnusual
		}
	} else if high && dominantConsistent {
		state = DiskWriteStateHigh
	}
	if observer.lastState == DiskWriteStateHigh || observer.lastState == DiskWriteStateUnusual {
		if ratio < diskWriteRecoveryRatio {
			if observer.recoverySince.IsZero() {
				observer.recoverySince = at
			}
		} else {
			observer.recoverySince = time.Time{}
		}
		if observer.recoverySince.IsZero() || at.Sub(observer.recoverySince) < diskWriteRecoveryDuration {
			state = observer.lastState
		}
	}
	// An incident remains anomalous throughout hysteretic recovery. Do not let
	// a 2x-6x recovery plateau enter the quiet histogram and ratchet p99 upward
	// until the same incident classifies as normal again.
	if state == DiskWriteStateHigh || state == DiskWriteStateUnusual ||
		observer.lastState == DiskWriteStateHigh || observer.lastState == DiskWriteStateUnusual {
		accepted = false
	}
	return state, accepted
}

func (observer *DiskWriteObserver) transitionLocked(summary DiskWriteSummary) *DiskWriteTransition {
	previous := observer.lastState
	observer.lastState = summary.State
	if previous == "" || previous == summary.State {
		return nil
	}
	if !observer.lastTransition.IsZero() && summary.CapturedAt.Sub(observer.lastTransition) < diskWriteTransitionInterval {
		return nil
	}
	observer.lastTransition = summary.CapturedAt
	return &DiskWriteTransition{
		SchemaVersion: 1, Timestamp: summary.CapturedAt, From: previous, To: summary.State,
		Confidence: summary.Confidence, Ratio: summary.BaselineRatio, WindowBytes: summary.Window15mBytes, Context: summary.Context,
	}
}

func (observer *DiskWriteObserver) alertLocked(summary DiskWriteSummary) *DiskWriteAlert {
	if observer.alertStateInvalid {
		return nil
	}
	day := summary.CapturedAt.Local().Format("20060102")
	if day != observer.alertDay {
		if !observer.saveAlertStateLocked(day, 0, observer.incidentOpen, summary.CapturedAt) {
			return nil
		}
		observer.alertDay, observer.alertsToday = day, 0
		observer.alertStateInvalid = false
	}
	if summary.State != DiskWriteStateHigh {
		if summary.State == DiskWriteStateNormal && observer.incidentOpen {
			if !observer.saveAlertStateLocked(observer.alertDay, observer.alertsToday, false, summary.CapturedAt) {
				return nil
			}
			observer.incidentOpen = false
		}
		return nil
	}
	if !observer.policy.NotificationsEnabled || observer.incidentOpen || observer.alertsToday >= diskWriteAlertLimitPerDay {
		return nil
	}
	if !observer.saveAlertStateLocked(observer.alertDay, observer.alertsToday+1, true, summary.CapturedAt) {
		return nil
	}
	observer.incidentOpen = true
	observer.alertsToday++
	writer := ""
	if summary.TopWriter != nil {
		writer = summary.TopWriter.Executable
	}
	return &DiskWriteAlert{Timestamp: summary.CapturedAt, State: summary.State, Ratio: summary.BaselineRatio, WindowBytes: summary.Window15mBytes, TopWriter: writer}
}

func (observer *DiskWriteObserver) saveAlertStateLocked(day string, alerts int, incidentOpen bool, at time.Time) bool {
	if observer.store == nil {
		return true
	}
	return observer.store.saveAlertState(diskWriteAlertStateRecord{
		Day: day, Alerts: alerts, IncidentOpen: incidentOpen, UpdatedAt: at.UTC(),
	}) == nil
}

func (observer *DiskWriteObserver) ensureCurrentHourLocked(at time.Time) {
	hour := at.Truncate(time.Hour)
	if observer.currentHour == nil || !observer.currentHour.Hour.Equal(hour) {
		observer.flushHourLocked()
		observer.currentHour = &diskWriteHistoryRecord{
			SchemaVersion: 1, ModelVersion: diskWriteModelVersion, Hour: hour,
			Contexts: make(map[string]diskHistogramRecord), executableHistograms: make(map[string]*diskHistogram),
		}
	}
}

func (observer *DiskWriteObserver) recordHourLocked(at time.Time, state DiskWriteState, p99 uint64, deviceDelta uint64, device diskDeviceCounter, hasPrevious, validWindow bool) {
	observer.ensureCurrentHourLocked(at)
	observer.currentHour.State = state
	if validWindow {
		observer.currentHour.BytesWritten = saturatingAddUint64(observer.currentHour.BytesWritten, deviceDelta)
		observer.currentHour.SampleCount++
	} else if hasPrevious {
		observer.currentHour.UnscoredGapBytes = saturatingAddUint64(observer.currentHour.UnscoredGapBytes, deviceDelta)
	}
	observer.currentHour.BaselineP99Bytes = p99
	observer.currentHour.LastSampleAt = at
	observer.currentHour.DeviceIdentity = device.Identity
	observer.currentHour.DeviceBytes = device.BytesWritten
	observer.currentHour.DeviceCount = device.DeviceCount
	observer.currentHour.DeviceSource = device.Source
}

func (observer *DiskWriteObserver) recordBaselineLocked(at time.Time, contextName string, value uint64, writers []DiskWriter) {
	observer.ensureCurrentHourLocked(at)
	histogram := diskHistogramFromRecord(observer.currentHour.Contexts[contextName])
	histogram.add(value, at)
	observer.currentHour.Contexts[contextName] = histogram.record()
	for index, writer := range writers {
		if index >= 8 {
			break
		}
		model := observer.executableModels[writer.Executable]
		if model == nil {
			model = &diskHistogram{}
			observer.executableModels[writer.Executable] = model
		}
		model.add(writer.WindowBytes, at)
		observer.executableBytes[writer.Executable] = saturatingAddUint64(observer.executableBytes[writer.Executable], writer.WindowBytes)
		hourModel := observer.currentHour.executableHistograms[writer.Executable]
		if hourModel == nil {
			hourModel = &diskHistogram{}
			observer.currentHour.executableHistograms[writer.Executable] = hourModel
		}
		hourModel.add(writer.WindowBytes, at)
	}
	observer.trimExecutableModels()
}

func (observer *DiskWriteObserver) trimExecutableModels() {
	if len(observer.executableModels) <= diskWriteMaxExecutableModels {
		return
	}
	names := make([]string, 0, len(observer.executableModels))
	for name := range observer.executableModels {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if observer.executableBytes[names[i]] != observer.executableBytes[names[j]] {
			return observer.executableBytes[names[i]] > observer.executableBytes[names[j]]
		}
		return names[i] < names[j]
	})
	for _, name := range names[diskWriteMaxExecutableModels:] {
		delete(observer.executableModels, name)
		delete(observer.executableBytes, name)
	}
}

func (observer *DiskWriteObserver) flushHourLocked() {
	if observer.currentHour == nil || observer.store == nil {
		return
	}
	record, ok := observer.currentHourRecordLocked()
	if !ok {
		observer.currentHour = nil
		return
	}
	if observer.store.appendRecord(record) != nil {
		_ = observer.store.writeCheckpoint(record)
		observer.currentHour = nil
		return
	}
	_ = observer.store.removeCheckpoint()
	_ = observer.store.Prune(observer.policy.BaselineRetentionDays)
	observer.currentHour = nil
}

func (observer *DiskWriteObserver) checkpointHourLocked() {
	if observer.currentHour == nil || observer.store == nil {
		return
	}
	record, ok := observer.currentHourRecordLocked()
	if !ok {
		return
	}
	_ = observer.store.writeCheckpoint(record)
}

func (observer *DiskWriteObserver) currentHourRecordLocked() (diskWriteHistoryRecord, bool) {
	if observer.currentHour == nil {
		return diskWriteHistoryRecord{}, false
	}
	record := *observer.currentHour
	if record.SampleCount == 0 && len(record.Contexts) == 0 && record.DeviceIdentity == "" {
		return diskWriteHistoryRecord{}, false
	}
	record.Executables = make([]diskExecutableHistory, 0, len(record.executableHistograms))
	for name, histogram := range record.executableHistograms {
		record.Executables = append(record.Executables, diskExecutableHistory{Executable: name, TotalBytes: histogram.Sum, Histogram: histogram.record()})
	}
	sort.Slice(record.Executables, func(i, j int) bool {
		if record.Executables[i].TotalBytes != record.Executables[j].TotalBytes {
			return record.Executables[i].TotalBytes > record.Executables[j].TotalBytes
		}
		return record.Executables[i].Executable < record.Executables[j].Executable
	})
	if len(record.Executables) > 8 {
		record.Executables = record.Executables[:8]
	}
	record.executableHistograms = nil
	return record, true
}

func (observer *DiskWriteObserver) Run(ctx context.Context, onTransition func(DiskWriteTransition), onAlert func(DiskWriteAlert)) error {
	if observer == nil {
		return errors.New("disk-write observer is nil")
	}
	if !observer.policy.Enabled {
		observer.Sample(ctx, observer.now())
		return nil
	}
	interval := time.Duration(observer.policy.SampleIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer func() {
		observer.mu.Lock()
		observer.checkpointHourLocked()
		observer.mu.Unlock()
	}()
	for {
		report, transition, alert := observer.Sample(ctx, observer.now())
		_ = report
		if transition != nil && onTransition != nil {
			onTransition(*transition)
		}
		if alert != nil && onAlert != nil {
			onAlert(*alert)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// LiveDiskWriteReport takes a bounded two-point native sample without
// persisting or training the resident baseline.
func LiveDiskWriteReport(ctx context.Context, dir string, policy DiskWritePolicy, window time.Duration) (DiskWriteReport, error) {
	if window <= 0 || window > 5*time.Second {
		return DiskWriteReport{}, fmt.Errorf("live disk-write window must be between 1ns and 5s")
	}
	observer := NewDiskWriteObserver(dir, policy)
	observer.allowShort = true
	observer.store = nil
	observer.Sample(ctx, time.Now().UTC())
	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return DiskWriteReport{}, ctx.Err()
	case <-timer.C:
	}
	report, _, _ := observer.Sample(ctx, time.Now().UTC())
	if report.Summary.State == DiskWriteStateUnavailable {
		return report, fmt.Errorf("disk-write counters unavailable")
	}
	return report, nil
}

func roundRatio(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	return math.Round(value*100) / 100
}
