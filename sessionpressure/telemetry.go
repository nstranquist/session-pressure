package sessionpressure

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/jsonl"
)

type TelemetryEvent struct {
	SchemaVersion int                       `json:"schema_version"`
	Timestamp     time.Time                 `json:"timestamp"`
	Event         string                    `json:"event"`
	Snapshot      *Snapshot                 `json:"snapshot,omitempty"`
	Summary       *TelemetrySnapshotSummary `json:"summary,omitempty"`
	Error         string                    `json:"error,omitempty"`
	DiskWrite     *DiskWriteTransition      `json:"disk_write,omitempty"`
}

// TelemetrySnapshotSummary is the compact steady-state heartbeat projection.
// Full live truth remains in latest.json and state transitions retain the
// bounded Snapshot contract; repeating dozens of mostly static fields every
// five minutes provided little diagnostic value and nearly consumed the daily
// telemetry budget.
//
// M2: wrapper_interrupt_* fields are the sparse main-plane interrupt forensics
// projection (counts only). They are omitted when daily telemetry budget is
// exhausted (FitsTelemetryBudget).
type TelemetrySnapshotSummary struct {
	SchemaVersion              int            `json:"schema_version"`
	Timestamp                  time.Time      `json:"timestamp"`
	Level                      Level          `json:"level"`
	PrimaryReason              string         `json:"primary_reason,omitempty"`
	FreePercent                int            `json:"free_percent"`
	SwapUsedMB                 float64        `json:"swap_used_mb"`
	HostCPUPercent             float64        `json:"host_cpu_percent"`
	AgentTreeCount             int            `json:"agent_tree_count"`
	AgentRSSSumMB              float64        `json:"agent_rss_sum_mb"`
	MemoryMomentum             MemoryMomentum `json:"memory_momentum"`
	FreePercentSlopePerMinute  float64        `json:"free_percent_slope_per_minute,omitempty"`
	MinutesToMemoryRed         *float64       `json:"minutes_to_memory_red,omitempty"`
	GuardRSSMB                 float64        `json:"guard_rss_mb"`
	GuardIdleCPUDutyPercent    float64        `json:"guard_idle_cpu_duty_percent,omitempty"`
	SampleDurationP95MS        float64        `json:"sample_duration_p95_ms,omitempty"`
	SampleCPUTimeP95MS         float64        `json:"sample_cpu_time_p95_ms,omitempty"`
	TelemetryProjectedBytesDay int64          `json:"telemetry_projected_bytes_per_day,omitempty"`
	GuardBudgetOK              bool           `json:"guard_budget_ok"`
	GuardSampleErrors          int            `json:"guard_sample_errors,omitempty"`
	ResourceCleanupFailures    int            `json:"resource_cleanup_failures,omitempty"`
	ResourceCleanupStatus      string         `json:"resource_cleanup_status,omitempty"`
	ResidentStarts24h          int            `json:"resident_starts_24h,omitempty"`
	// WrapperInterruptOperations is the 24h closed-enum interrupt count (M2).
	WrapperInterruptOperations int `json:"wrapper_interrupt_operations,omitempty"`
	// WrapperInterruptRatePerHour is ops/hour over the same window (M2).
	WrapperInterruptRatePerHour float64 `json:"wrapper_interrupt_rate_per_hour,omitempty"`
}

// wrapperInterruptTelemetryWindow is the sparse main-plane lookback for
// interrupt forensics embedded on heartbeats.
const wrapperInterruptTelemetryWindow = 24 * time.Hour

func compactTelemetrySummary(snapshot Snapshot) TelemetrySnapshotSummary {
	primaryReason := ""
	if len(snapshot.Reasons) > 0 {
		primaryReason = boundedText(snapshot.Reasons[0], actionReasonLimit)
	}
	return TelemetrySnapshotSummary{
		SchemaVersion: SchemaVersion, Timestamp: snapshot.Timestamp, Level: snapshot.Level,
		PrimaryReason: primaryReason, FreePercent: snapshot.FreePercent, SwapUsedMB: snapshot.SwapUsedMB,
		HostCPUPercent: snapshot.HostCPUPercent, AgentTreeCount: snapshot.AgentTreeCount, AgentRSSSumMB: snapshot.AgentRSSSumMB,
		MemoryMomentum: snapshot.MemoryMomentum, FreePercentSlopePerMinute: snapshot.FreePercentSlopePerMinute,
		MinutesToMemoryRed: snapshot.MinutesToMemoryRed,
		GuardRSSMB:         snapshot.GuardRSSMB, GuardIdleCPUDutyPercent: snapshot.GuardIdleCPUDutyPercent,
		SampleDurationP95MS: snapshot.SampleDurationP95MS, SampleCPUTimeP95MS: snapshot.SampleCPUTimeP95MS,
		TelemetryProjectedBytesDay: snapshot.TelemetryProjectedBytesDay, GuardBudgetOK: snapshot.GuardBudgetOK,
		GuardSampleErrors: snapshot.GuardSampleErrors, ResourceCleanupFailures: snapshot.ResourceCleanupFailures,
		ResourceCleanupStatus: snapshot.ResourceCleanupStatus, ResidentStarts24h: snapshot.ResidentStarts24h,
	}
}

// Projection reserves are deliberately conservative and shared by the live
// monitor, the evaluator, and audit output. Keeping one formula prevents a
// compact synthetic fixture from reporting a materially different daily cost
// than the resident enforces.
const (
	projectedRecurringEventBytes    int64 = 1024
	projectedTransitionEventBytes   int64 = 8 << 10
	maxProjectedActionRecordBytes   int64 = 1000
	projectedDiskWriteEventBytes    int64 = 384
	projectedDiskWriteEventsDay     int64 = 96
	reliefRevalidationRetryInterval       = 3 * time.Minute
)

type TelemetryDailyProjection struct {
	RecurringEventBytes  int64 `json:"recurring_event_bytes"`
	RecurringEventsDay   int64 `json:"recurring_events_per_day"`
	TransitionEventBytes int64 `json:"transition_event_bytes"`
	TransitionEventsDay  int64 `json:"transition_events_per_day"`
	ActionEventBytes     int64 `json:"action_event_bytes,omitempty"`
	ActionEventsDay      int64 `json:"action_events_per_day,omitempty"`
	MonitorBytes         int64 `json:"monitor_bytes_per_day"`
	TotalBytes           int64 `json:"total_bytes_per_day"`
	DiskWriteEventBytes  int64 `json:"disk_write_event_bytes,omitempty"`
	DiskWriteEventsDay   int64 `json:"disk_write_events_per_day,omitempty"`
}

func ProjectTelemetryBytesPerDay(policy Policy, recurringObservedBytes, transitionObservedBytes, actionObservedBytes int64) TelemetryDailyProjection {
	projection := TelemetryDailyProjection{
		RecurringEventBytes:  max(recurringObservedBytes, projectedRecurringEventBytes),
		TransitionEventBytes: max(transitionObservedBytes, projectedTransitionEventBytes),
		TransitionEventsDay:  24,
	}
	if policy.HeartbeatSeconds > 0 {
		projection.RecurringEventsDay = int64(86400 / policy.HeartbeatSeconds)
	}
	if policy.ActionCooldownSeconds > 0 {
		actionInterval := time.Duration(policy.ActionCooldownSeconds) * time.Second
		projection.ActionEventBytes = actionObservedBytes
		if policy.AutoShedCritical {
			projection.ActionEventBytes = max(projection.ActionEventBytes, maxProjectedActionRecordBytes)
			actionInterval = min(actionInterval, reliefRevalidationRetryInterval)
		}
		if projection.ActionEventBytes > 0 && actionInterval > 0 {
			projection.ActionEventsDay = int64((24 * time.Hour) / actionInterval)
		}
	}
	projection.MonitorBytes = projection.RecurringEventBytes*projection.RecurringEventsDay +
		projection.TransitionEventBytes*projection.TransitionEventsDay
	if policy.DiskWrite.Enabled {
		projection.DiskWriteEventBytes = projectedDiskWriteEventBytes
		projection.DiskWriteEventsDay = projectedDiskWriteEventsDay
	}
	projection.TotalBytes = projection.MonitorBytes + projection.ActionEventBytes*projection.ActionEventsDay +
		projection.DiskWriteEventBytes*projection.DiskWriteEventsDay
	return projection
}

// attachWrapperInterruptToSummary projects closed interrupt counts onto the
// main sparse heartbeat plane when the daily telemetry budget still has room.
// No-ops on error or budget pressure (fail closed: omit fields, never invent).
func attachWrapperInterruptToSummary(summary *TelemetrySnapshotSummary, store *TelemetryStore, policy Policy, now time.Time) {
	if summary == nil || store == nil {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	since := now.Add(-wrapperInterruptTelemetryWindow)
	interrupts, err := NewWorkEventStore(store.Dir).CountWrapperInterruptOperations(since, now)
	if err != nil {
		return
	}
	proj, err := ProjectWrapperInterruptTelemetry(interrupts, wrapperInterruptTelemetryWindow, now)
	if err != nil {
		return
	}
	maxBytes := policy.ResourceBudgets.MaxTelemetryBytesDay
	if maxBytes <= 0 {
		maxBytes = DefaultPolicy(16 << 10).ResourceBudgets.MaxTelemetryBytesDay
	}
	bytesToday := store.BytesForDay(now)
	if !FitsTelemetryBudget(bytesToday, int64(proj.ProjectedBytes), maxBytes) {
		return
	}
	// Only emit when non-zero to keep heartbeats sparse; zero is the default omitempty.
	if proj.WrapperInterruptOperations <= 0 {
		return
	}
	summary.WrapperInterruptOperations = proj.WrapperInterruptOperations
	summary.WrapperInterruptRatePerHour = proj.WrapperInterruptRatePerHour
}

type TelemetryStore struct {
	Dir string
	Now func() time.Time
}

// Durable state transitions retain aggregate totals plus the two largest
// trees. Steady-state heartbeats use TelemetrySnapshotSummary, while the
// unsharded latest projection still carries the full bounded inventory for
// live admission and relief decisions.
const telemetryTopTreeLimit = 2
const telemetryTopHostConsumerLimit = 2
const durableTextLimit = 512
const actionReasonLimit = 96
const actionIdentityLimit = 64

func boundedDurableText(value string) string {
	return boundedText(value, durableTextLimit)
}

func boundedText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func NewTelemetryStore(dir string) *TelemetryStore {
	return &TelemetryStore{Dir: dir, Now: time.Now}
}

func (store *TelemetryStore) dayPath(t time.Time) string {
	return filepath.Join(store.Dir, "snapshots-"+t.Local().Format("20060102")+".jsonl")
}

func (store *TelemetryStore) actionDayPath(t time.Time) string {
	return filepath.Join(store.Dir, "actions-"+t.Local().Format("20060102")+".jsonl")
}

func (store *TelemetryStore) LatestPath() string  { return filepath.Join(store.Dir, "latest.json") }
func (store *TelemetryStore) ActionsPath() string { return store.actionDayPath(store.Now()) }

func (store *TelemetryStore) legacyActionsPath() string {
	return filepath.Join(store.Dir, "actions.jsonl")
}

func (store *TelemetryStore) AppendEvent(event TelemetryEvent) error {
	if event.SchemaVersion == 0 {
		event.SchemaVersion = SchemaVersion
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = store.Now().UTC()
	}
	event.Error = boundedDurableText(event.Error)
	if event.DiskWrite != nil {
		bounded := *event.DiskWrite
		bounded.Context = boundedText(bounded.Context, 24)
		if math.IsNaN(bounded.Ratio) || math.IsInf(bounded.Ratio, 0) || bounded.Ratio < 0 {
			bounded.Ratio = 0
		}
		event.DiskWrite = &bounded
	}
	if event.Event == "heartbeat" && event.Snapshot != nil {
		summary := compactTelemetrySummary(*event.Snapshot)
		// M2: sparse main-plane interrupt forensics on heartbeats (budget-gated).
		policy, _, policyErr := LoadPolicy(PolicyPath(store.Dir), 0)
		if policyErr != nil {
			policy = DefaultPolicy(16 << 10)
		}
		attachWrapperInterruptToSummary(&summary, store, policy, event.Timestamp)
		event.Summary = &summary
		event.Snapshot = nil
	}
	if event.Snapshot != nil && len(event.Snapshot.TopAgentTrees) > telemetryTopTreeLimit {
		bounded := *event.Snapshot
		bounded.TopAgentTrees = append([]AgentTree(nil), event.Snapshot.TopAgentTrees[:telemetryTopTreeLimit]...)
		event.Snapshot = &bounded
	}
	if event.Snapshot != nil && len(event.Snapshot.TopHostConsumers) > telemetryTopHostConsumerLimit {
		bounded := *event.Snapshot
		bounded.TopHostConsumers = append([]HostConsumer(nil), event.Snapshot.TopHostConsumers[:telemetryTopHostConsumerLimit]...)
		event.Snapshot = &bounded
	}
	// Writer identity belongs only in latest/live output. State-transition
	// telemetry may retain the compact DiskWrite summary but never PIDs or a
	// repeated writer list.
	if event.Snapshot != nil && len(event.Snapshot.DiskWriteWriters) > 0 {
		bounded := *event.Snapshot
		bounded.DiskWriteWriters = nil
		event.Snapshot = &bounded
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return jsonl.AppendLine(store.dayPath(event.Timestamp), body, 0o600)
}

func (store *TelemetryStore) WriteLatest(snapshot Snapshot) error {
	body, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWrite(store.LatestPath(), body, 0o600)
}

func (store *TelemetryStore) ReadLatest() (Snapshot, bool) {
	body, err := os.ReadFile(store.LatestPath())
	if err != nil {
		return Snapshot{}, false
	}
	var snapshot Snapshot
	if json.Unmarshal(body, &snapshot) != nil {
		return Snapshot{}, false
	}
	if snapshot.MemoryMomentum == "" {
		snapshot.MemoryMomentum = MemoryMomentumUnknown
	}
	if snapshot.TopHostConsumers == nil {
		snapshot.TopHostConsumers = []HostConsumer{}
	}
	return snapshot, true
}

func (store *TelemetryStore) AppendAction(action Action) error {
	if action.SchemaVersion == 0 {
		action.SchemaVersion = SchemaVersion
	}
	if action.Timestamp.IsZero() {
		action.Timestamp = store.Now().UTC()
	}
	action.Agent = boundedText(action.Agent, 32)
	action.SessionID = boundedText(action.SessionID, actionIdentityLimit)
	action.PrimaryHostExecutable = boundedText(action.PrimaryHostExecutable, 64)
	action.Reason = boundedText(action.Reason, actionReasonLimit)
	body, err := json.Marshal(action)
	if err != nil {
		return err
	}
	return jsonl.AppendLine(store.actionDayPath(action.Timestamp), body, 0o600)
}

// AppendActionDurable fsyncs an action row before acknowledging it. It keeps a
// separate cold path so the resident monitor's high-frequency AppendAction
// implementation and overhead remain unchanged.
func (store *TelemetryStore) AppendActionDurable(action Action) error {
	if action.SchemaVersion == 0 {
		action.SchemaVersion = SchemaVersion
	}
	if action.Timestamp.IsZero() {
		action.Timestamp = store.Now().UTC()
	}
	action.Agent = boundedText(action.Agent, 32)
	action.SessionID = boundedText(action.SessionID, actionIdentityLimit)
	action.PrimaryHostExecutable = boundedText(action.PrimaryHostExecutable, 64)
	action.Reason = boundedText(action.Reason, actionReasonLimit)
	body, err := json.Marshal(action)
	if err != nil {
		return err
	}
	return jsonl.AppendLineDurable(store.actionDayPath(action.Timestamp), body, 0o600)
}

func (store *TelemetryStore) BytesForDay(t time.Time) int64 {
	var total int64
	for _, path := range []string{store.dayPath(t), store.actionDayPath(t)} {
		if info, err := os.Stat(path); err == nil {
			total += info.Size()
		}
	}
	return total
}

func (store *TelemetryStore) LastAction() (Action, bool, error) {
	actions, err := store.ReadActions(1, time.Time{})
	if err != nil {
		return Action{}, false, err
	}
	if len(actions) == 0 {
		return Action{}, false, nil
	}
	return actions[len(actions)-1], true, nil
}

// ReadActions returns the newest bounded action audit rows across retained
// daily shards and the legacy unsharded ledger. Rows are sorted by their typed
// timestamp so a legacy file cannot disturb chronology.
func (store *TelemetryStore) ReadActions(limit int, since time.Time) ([]Action, error) {
	paths, err := filepath.Glob(filepath.Join(store.Dir, "actions-*.jsonl"))
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(store.legacyActionsPath()); statErr == nil {
		paths = append(paths, store.legacyActionsPath())
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect legacy action ledger: %w", statErr)
	}
	sort.Strings(paths)
	rows := make([]Action, 0)
	for _, path := range paths {
		fileRows, readErr := readActionRows(path, since)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return nil, readErr
		}
		rows = append(rows, fileRows...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Timestamp.Before(rows[j].Timestamp) })
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	return rows, nil
}

func readActionRows(path string, since time.Time) (rows []Action, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open action ledger %s: %w", filepath.Base(path), err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close action ledger %s: %w", filepath.Base(path), closeErr))
		}
	}()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		var action Action
		if decodeErr := json.Unmarshal(scanner.Bytes(), &action); decodeErr != nil {
			return nil, fmt.Errorf("decode action ledger %s line %d: %w", filepath.Base(path), line, decodeErr)
		}
		if action.SchemaVersion != SchemaVersion || action.Timestamp.IsZero() || strings.TrimSpace(action.Kind) == "" || strings.TrimSpace(action.Result) == "" {
			return nil, fmt.Errorf("invalid action ledger %s line %d", filepath.Base(path), line)
		}
		if since.IsZero() || !action.Timestamp.Before(since) {
			rows = append(rows, action)
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return nil, fmt.Errorf("scan action ledger %s: %w", filepath.Base(path), scanErr)
	}
	return rows, nil
}

func (store *TelemetryStore) ReadEvents(limit int, since time.Time) ([]TelemetryEvent, error) {
	matches, err := filepath.Glob(filepath.Join(store.Dir, "snapshots-*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	rows := make([]TelemetryEvent, 0)
	for _, path := range matches {
		file, err := os.Open(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			var event TelemetryEvent
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				continue
			}
			if !since.IsZero() && event.Timestamp.Before(since) {
				continue
			}
			rows = append(rows, event)
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	return rows, nil
}

func (store *TelemetryStore) Prune(retentionDays int) error {
	if retentionDays < 1 {
		return fmt.Errorf("retention must be positive")
	}
	cutoff := store.Now().AddDate(0, 0, -retentionDays)
	cutoffDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.Local)
	for _, prefix := range []string{"snapshots-", "actions-"} {
		matches, err := filepath.Glob(filepath.Join(store.Dir, prefix+"*.jsonl"))
		if err != nil {
			return err
		}
		for _, path := range matches {
			base := filepath.Base(path)
			dateText := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".jsonl")
			day, parseErr := time.ParseInLocation("20060102", dateText, time.Local)
			if parseErr != nil || !day.Before(cutoffDay) {
				continue
			}
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return removeErr
			}
		}
	}
	return store.pruneLegacyActions(cutoff)
}

func (store *TelemetryStore) pruneLegacyActions(cutoff time.Time) error {
	path := store.legacyActionsPath()
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var retained bytes.Buffer
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var action Action
		if json.Unmarshal(scanner.Bytes(), &action) == nil && !action.Timestamp.Before(cutoff) {
			retained.Write(scanner.Bytes())
			retained.WriteByte('\n')
		}
	}
	scanErr := scanner.Err()
	_ = file.Close()
	if scanErr != nil {
		return scanErr
	}
	if retained.Len() == 0 {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return atomicWrite(path, retained.Bytes(), 0o600)
}
