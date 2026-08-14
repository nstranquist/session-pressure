package sessionpressure

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
	"github.com/nstranquist/session-pressure/internal/jsonl"
)

const (
	WorkEventSchemaVersion        = 4
	minimumWorkEventSchemaVersion = 1
)

type WorkEventType string

const (
	WorkEventQueued            WorkEventType = "queued"
	WorkEventAcquired          WorkEventType = "acquired"
	WorkEventReserved          WorkEventType = "reserved"
	WorkEventReacquired        WorkEventType = "reacquired"
	WorkEventStarted           WorkEventType = "started"
	WorkEventCompleted         WorkEventType = "completed"
	WorkEventFailed            WorkEventType = "failed"
	WorkEventCancelled         WorkEventType = "cancelled"
	WorkEventExpired           WorkEventType = "expired"
	WorkEventReused            WorkEventType = "reused"
	WorkEventOverrideRequested WorkEventType = "override_requested"
)

func ParseWorkEventType(value string) (WorkEventType, error) {
	eventType := WorkEventType(strings.ToLower(strings.TrimSpace(value)))
	switch eventType {
	case WorkEventQueued, WorkEventOverrideRequested, WorkEventAcquired, WorkEventReserved, WorkEventReacquired, WorkEventStarted, WorkEventCompleted, WorkEventFailed, WorkEventCancelled, WorkEventExpired, WorkEventReused:
		return eventType, nil
	default:
		return "", fmt.Errorf("unknown work event %q; want queued, override_requested, acquired, reserved, reacquired, started, completed, failed, cancelled, expired, or reused", value)
	}
}

type WorkBlocker string

const (
	WorkBlockerNone        WorkBlocker = "none"
	WorkBlockerPressure    WorkBlocker = "pressure"
	WorkBlockerCapacity    WorkBlocker = "capacity"
	WorkBlockerFairness    WorkBlocker = "fairness"
	WorkBlockerReservation WorkBlocker = "reservation"
	WorkBlockerUpgrade     WorkBlocker = "upgrade"
)

type ExpressReuseDecision string

const (
	ExpressReuseDecisionReused ExpressReuseDecision = "reused"
	ExpressReuseDecisionRan    ExpressReuseDecision = "ran"
)

type ExpressReuseRefusalReason string

const (
	ExpressReuseStaleArtifact      ExpressReuseRefusalReason = "stale_artifact"
	ExpressReuseMissingArtifact    ExpressReuseRefusalReason = "missing_artifact"
	ExpressReuseSourceChanged      ExpressReuseRefusalReason = "source_changed"
	ExpressReuseTTLExpired         ExpressReuseRefusalReason = "ttl_expired"
	ExpressReuseDisabled           ExpressReuseRefusalReason = "disabled"
	ExpressReuseReceiptUnavailable ExpressReuseRefusalReason = "receipt_unavailable"
	ExpressReuseSingleflightOnly   ExpressReuseRefusalReason = "singleflight_only"
	// ExpressReuseUndeclaredBinary marks a `go build` of a main package with no
	// -o: Go writes a binary into the working directory under a name this guard
	// does not model, so there is no artifact to verify and replaying success
	// could mask a stale or missing binary. Distinct from receipt_unavailable so
	// telemetry can size this population instead of lumping it with I/O failures.
	ExpressReuseUndeclaredBinary ExpressReuseRefusalReason = "undeclared_binary"
)

// WorkEvent is the privacy-bounded audit row for one heavy-work lifecycle.
// CommandDigest covers only the resolved executable identity and argument
// count; arguments, environment, cwd, and prompts are never serialized.
type WorkEvent struct {
	SchemaVersion                       int                       `json:"schema_version"`
	EventID                             string                    `json:"event_id"`
	RequestID                           string                    `json:"request_id,omitempty"`
	Timestamp                           time.Time                 `json:"timestamp"`
	Event                               WorkEventType             `json:"event"`
	OperationID                         string                    `json:"operation_id"`
	LeaseID                             string                    `json:"lease_id,omitempty"`
	Class                               WorkClass                 `json:"class"`
	RequestedClass                      WorkClass                 `json:"requested_class,omitempty"`
	Weight                              int                       `json:"weight"`
	PID                                 int                       `json:"pid,omitempty"`
	SessionDigest                       string                    `json:"session_digest,omitempty"`
	CommandDigest                       string                    `json:"command_digest,omitempty"`
	Blocker                             WorkBlocker               `json:"blocker,omitempty"`
	QueuePosition                       int                       `json:"queue_position,omitempty"`
	QueueDepth                          int                       `json:"queue_depth,omitempty"`
	Capacity                            int                       `json:"capacity,omitempty"`
	Used                                int                       `json:"used,omitempty"`
	Available                           int                       `json:"available,omitempty"`
	WaitMilliseconds                    int64                     `json:"wait_ms,omitempty"`
	AdmissionWaitMilliseconds           int64                     `json:"admission_wait_ms,omitempty"`
	QueueWaitMilliseconds               int64                     `json:"queue_wait_ms,omitempty"`
	PressureWaitMilliseconds            int64                     `json:"pressure_wait_ms,omitempty"`
	PrestartMilliseconds                int64                     `json:"prestart_ms,omitempty"`
	RuntimeMillis                       int64                     `json:"runtime_ms,omitempty"`
	PressureLevel                       Level                     `json:"pressure_level,omitempty"`
	PressureDimension                   string                    `json:"pressure_dimension,omitempty"`
	PressureReason                      string                    `json:"pressure_reason,omitempty"`
	CoordinatedWorkAttributionAvailable bool                      `json:"coordinated_work_attribution_available,omitempty"`
	CoordinatedWorkCPUAvailable         bool                      `json:"coordinated_work_cpu_available,omitempty"`
	CoordinatedWorkCPUPercent           float64                   `json:"coordinated_work_cpu_percent,omitempty"`
	CoordinatedWorkLeaseCount           int                       `json:"coordinated_work_lease_count,omitempty"`
	CoordinatedWorkProcessCount         int                       `json:"coordinated_work_process_count,omitempty"`
	CoordinatedWorkInventoryAgeSeconds  float64                   `json:"coordinated_work_inventory_age_seconds,omitempty"`
	ExitCode                            *int                      `json:"exit_code,omitempty"`
	Outcome                             string                    `json:"outcome,omitempty"`
	SchedulingPolicy                    string                    `json:"scheduling_policy,omitempty"`
	SelectorSchemaVersion               int                       `json:"selector_schema_version,omitempty"`
	SelectedOperationID                 string                    `json:"selected_operation_id,omitempty"`
	ProtectedOperationID                string                    `json:"protected_operation_id,omitempty"`
	DecisionReason                      string                    `json:"decision_reason,omitempty"`
	BypassedCount                       int                       `json:"bypassed_count,omitempty"`
	CandidateSchedulingPolicy           string                    `json:"candidate_scheduling_policy,omitempty"`
	ShadowSelectedOperationID           string                    `json:"shadow_selected_operation_id,omitempty"`
	ShadowProtectedOperationID          string                    `json:"shadow_protected_operation_id,omitempty"`
	ShadowDecisionReason                string                    `json:"shadow_decision_reason,omitempty"`
	BatchStepCount                      int                       `json:"batch_step_count,omitempty"`
	BatchCompletedSteps                 int                       `json:"batch_completed_steps,omitempty"`
	ReuseStatus                         string                    `json:"reuse_status,omitempty"`
	ReuseDecision                       ExpressReuseDecision      `json:"reuse_decision,omitempty"`
	ReuseRefusalReason                  ExpressReuseRefusalReason `json:"reuse_refusal_reason,omitempty"`
	ReuseKeyDigest                      string                    `json:"reuse_key_digest,omitempty"`
	ReceiptDigest                       string                    `json:"receipt_digest,omitempty"`
	SingleflightWaitMS                  int64                     `json:"singleflight_wait_ms,omitempty"`
	// AdmissionDecision records why the pressure gate admitted or deferred this
	// operation. It keeps fast-lane and warning-derating populations measurable
	// and revocable from telemetry alone.
	AdmissionDecision string `json:"admission_decision,omitempty"`
}

func (event WorkEvent) Validate() error {
	if event.SchemaVersion < minimumWorkEventSchemaVersion || event.SchemaVersion > WorkEventSchemaVersion {
		return fmt.Errorf("unsupported work event schema_version %d", event.SchemaVersion)
	}
	if event.Timestamp.IsZero() {
		return errors.New("work event timestamp is required")
	}
	if !validPrivateID(event.EventID) {
		return errors.New("work event_id must be a 32-character lowercase hex identity")
	}
	if !validPrivateID(event.OperationID) {
		return errors.New("work operation_id must be a 32-character lowercase hex identity")
	}
	if event.LeaseID != "" && !validPrivateID(event.LeaseID) {
		return errors.New("work lease_id must be a 32-character lowercase hex identity")
	}
	if event.SessionDigest != "" && !validSHA256Digest(event.SessionDigest) {
		return errors.New("work session_digest must be an opaque sha256 digest")
	}
	if event.CommandDigest != "" && !validSHA256Digest(event.CommandDigest) {
		return errors.New("work command_digest must be an opaque sha256 digest")
	}
	if event.ReceiptDigest != "" && !validSHA256Digest(event.ReceiptDigest) {
		return errors.New("work receipt_digest must be an opaque sha256 digest")
	}
	if event.ReuseKeyDigest != "" && !validSHA256Digest(event.ReuseKeyDigest) {
		return errors.New("work reuse_key_digest must be an opaque sha256 digest")
	}
	if event.ReuseDecision != "" && event.ReuseDecision != ExpressReuseDecisionReused && event.ReuseDecision != ExpressReuseDecisionRan {
		return errors.New("work reuse_decision must be reused or ran")
	}
	switch event.ReuseRefusalReason {
	case "", ExpressReuseStaleArtifact, ExpressReuseMissingArtifact, ExpressReuseSourceChanged, ExpressReuseTTLExpired, ExpressReuseDisabled, ExpressReuseReceiptUnavailable, ExpressReuseSingleflightOnly, ExpressReuseUndeclaredBinary:
	default:
		return errors.New("work reuse_refusal_reason is invalid")
	}
	if event.ReuseRefusalReason != "" && event.ReuseDecision != ExpressReuseDecisionRan {
		return errors.New("work reuse refusal requires reuse_decision ran")
	}
	if _, err := ParseWorkClass(string(event.Class)); err != nil {
		return err
	}
	if event.RequestedClass != "" {
		if _, err := ParseWorkClass(string(event.RequestedClass)); err != nil {
			return fmt.Errorf("work requested_class: %w", err)
		}
		if event.RequestedClass == event.Class {
			return errors.New("work requested_class must be omitted when it matches class")
		}
	}
	if event.RequestID != "" && !validPrivateID(event.RequestID) {
		return errors.New("work event request_id must be a 32-character lowercase hex identity")
	}
	if event.Weight < 1 || event.WaitMilliseconds < 0 || event.AdmissionWaitMilliseconds < 0 || event.QueueWaitMilliseconds < 0 || event.PressureWaitMilliseconds < 0 || event.PrestartMilliseconds < 0 || event.RuntimeMillis < 0 || event.QueuePosition < 0 || event.QueueDepth < 0 || event.Capacity < 0 || event.Used < 0 || event.Available < 0 || event.BypassedCount < 0 || event.BatchStepCount < 0 || event.BatchCompletedSteps < 0 || event.BatchCompletedSteps > event.BatchStepCount || event.SingleflightWaitMS < 0 || event.CoordinatedWorkLeaseCount < 0 || event.CoordinatedWorkProcessCount < 0 {
		return errors.New("work event weight must be positive and counters or durations cannot be negative")
	}
	if math.IsNaN(event.CoordinatedWorkCPUPercent) || math.IsInf(event.CoordinatedWorkCPUPercent, 0) || event.CoordinatedWorkCPUPercent < 0 || event.CoordinatedWorkCPUPercent > 100 {
		return errors.New("work event coordinated CPU percent must be between 0 and 100")
	}
	if math.IsNaN(event.CoordinatedWorkInventoryAgeSeconds) || math.IsInf(event.CoordinatedWorkInventoryAgeSeconds, 0) || event.CoordinatedWorkInventoryAgeSeconds < 0 {
		return errors.New("work event coordinated inventory age cannot be negative or non-finite")
	}
	if event.CoordinatedWorkCPUAvailable && !event.CoordinatedWorkAttributionAvailable {
		return errors.New("work event coordinated CPU requires available attribution")
	}
	if _, err := ParseWorkEventType(string(event.Event)); err != nil {
		return err
	}
	if event.Blocker != "" {
		switch event.Blocker {
		case WorkBlockerNone, WorkBlockerPressure, WorkBlockerCapacity, WorkBlockerFairness, WorkBlockerReservation, WorkBlockerUpgrade:
		default:
			return fmt.Errorf("unknown work blocker %q", event.Blocker)
		}
	}
	if event.PressureDimension != "" && event.PressureDimension != "cpu" && event.PressureDimension != "memory" && event.PressureDimension != "storage" && event.PressureDimension != "unknown" {
		return fmt.Errorf("unknown work pressure_dimension %q", event.PressureDimension)
	}
	if (event.Event == WorkEventAcquired || event.Event == WorkEventReserved || event.Event == WorkEventReacquired || event.Event == WorkEventStarted || event.Event == WorkEventCompleted) && event.LeaseID == "" {
		return fmt.Errorf("work %s event requires lease_id", event.Event)
	}
	if event.Event == WorkEventQueued && event.LeaseID != "" {
		return errors.New("work queued event cannot contain lease_id")
	}
	return nil
}

func validPrivateID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && value == strings.ToLower(value)
}

func validSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func NewWorkOperationID() (string, error) { return randomPrivateID() }

func randomPrivateID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate work identity: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}

func workEventID(operationID string, eventType WorkEventType, leaseID string) string {
	digest := sha256.Sum256([]byte(operationID + "\x00" + string(eventType) + "\x00" + leaseID))
	return hex.EncodeToString(digest[:16])
}

func workEventIdentity(event WorkEvent) string {
	if event.Event == WorkEventOverrideRequested {
		// New override events carry a cryptographic request identity so two
		// distinct requests remain auditable even when the wall clock has the
		// same value. Timestamp fallback preserves identity for schema-3 rows
		// written before request_id was introduced.
		identity := event.RequestID
		if identity == "" {
			identity = event.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		digest := sha256.Sum256([]byte(event.OperationID + "\x00" + string(event.Event) + "\x00" + identity))
		return hex.EncodeToString(digest[:16])
	}
	return workEventID(event.OperationID, event.Event, event.LeaseID)
}

func CommandShapeDigest(resolvedExecutable string, argumentCount int) string {
	shape := strings.TrimSpace(resolvedExecutable) + "\x00argc=" + fmt.Sprint(max(0, argumentCount))
	digest := sha256.Sum256([]byte(shape))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func DetectedAgentSessionDigest(environment []string) string {
	for _, key := range []string{"CODEX_THREAD_ID", "CLAUDE_SESSION_ID", "GROK_SESSION_ID", "KIMI_SESSION_ID"} {
		prefix := key + "="
		for _, value := range environment {
			if strings.HasPrefix(value, prefix) {
				raw := strings.TrimSpace(strings.TrimPrefix(value, prefix))
				if raw == "" {
					continue
				}
				digest := sha256.Sum256([]byte(key + "\x00" + raw))
				return "sha256:" + hex.EncodeToString(digest[:])
			}
		}
	}
	return ""
}

type WorkEventFilter struct {
	Since time.Time
	Limit int
	Class WorkClass
	Event WorkEventType
	// OperationID narrows the ledger to one operation's lifecycle. Without it a
	// single-operation inspector has to read the whole window and discard nearly
	// all of it.
	OperationID string
}

type WorkEventStore struct {
	Dir string
	Now func() time.Time
}

func NewWorkEventStore(dir string) *WorkEventStore {
	return &WorkEventStore{Dir: dir, Now: time.Now}
}

func (store *WorkEventStore) dayPath(at time.Time) string {
	return filepath.Join(store.Dir, "work-events-"+at.Local().Format("20060102")+".jsonl")
}

func (store *WorkEventStore) AppendDurable(event WorkEvent) error {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return errors.New("work event store directory is required")
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = WorkEventSchemaVersion
	}
	now := time.Now
	if store.Now != nil {
		now = store.Now
	}
	current := now().UTC()
	if event.Timestamp.IsZero() {
		event.Timestamp = current
	}
	if event.Timestamp.After(current.Add(maximumWorkClockSkew)) {
		return fmt.Errorf("work event timestamp %s is beyond allowed clock skew", event.Timestamp.Format(time.RFC3339Nano))
	}
	event.EventID = workEventIdentity(event)
	event.SessionDigest = boundedText(event.SessionDigest, 80)
	event.CommandDigest = boundedText(event.CommandDigest, 80)
	event.PressureDimension = boundedText(event.PressureDimension, 32)
	event.PressureReason = boundedText(event.PressureReason, actionReasonLimit)
	event.Outcome = boundedText(event.Outcome, 64)
	if err := event.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	path := store.dayPath(event.Timestamp)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// jsonl.AppendLineDurable owns the shard lock. A separate identity lock
	// serializes the read-before-append idempotency check without recursively
	// acquiring the same non-reentrant file lock.
	unlock, err := filelock.AcquireContext(ctx, path+".identity", 5*time.Second)
	if err != nil {
		return fmt.Errorf("lock work event ledger: %w", err)
	}
	defer unlock()
	exists, err := workEventIDExists(path, event.EventID)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return jsonl.AppendLineDurable(path, body, 0o600)
}

func workEventIDExists(path, eventID string) (exists bool, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var identity struct {
			EventID string `json:"event_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &identity); err != nil {
			return false, fmt.Errorf("decode work event identity: %w", err)
		}
		if identity.EventID == eventID {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func (store *WorkEventStore) operationHasTerminal(operationID string) (bool, error) {
	paths, err := filepath.Glob(filepath.Join(store.Dir, "work-events-*.jsonl"))
	if err != nil {
		return false, err
	}
	for _, path := range paths {
		rows, err := readWorkEventRows(path, WorkEventFilter{})
		if err != nil {
			return false, err
		}
		for _, event := range rows {
			if event.OperationID == operationID && isWorkTerminalEvent(event.Event) {
				return true, nil
			}
		}
	}
	return false, nil
}

// Read returns the filtered event corpus, silently dropping any operation whose
// lifecycle does not validate. Callers that need to report the drop should use
// ReadWithDiagnostics instead.
func (store *WorkEventStore) Read(filter WorkEventFilter) ([]WorkEvent, error) {
	rows, _, err := store.ReadWithDiagnostics(filter)
	return rows, err
}

// ReadWithDiagnostics returns the filtered event corpus plus what it had to drop.
// Structural faults that make the whole shard untrustworthy — unreadable files,
// undecodable rows, duplicate event IDs, impossible timestamps — still fail
// closed; only per-operation lifecycle faults are tolerated.
func (store *WorkEventStore) ReadWithDiagnostics(filter WorkEventFilter) ([]WorkEvent, WorkEventDiagnostics, error) {
	rows, diagnostics, err := store.readAll(filter)
	if err != nil {
		return nil, WorkEventDiagnostics{}, err
	}
	return rows, diagnostics, nil
}

func (store *WorkEventStore) readAll(filter WorkEventFilter) ([]WorkEvent, WorkEventDiagnostics, error) {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return nil, WorkEventDiagnostics{}, errors.New("work event store directory is required")
	}
	if filter.Event != "" {
		if _, err := ParseWorkEventType(string(filter.Event)); err != nil {
			return nil, WorkEventDiagnostics{}, err
		}
	}
	if filter.Class != "" {
		if _, err := ParseWorkClass(string(filter.Class)); err != nil {
			return nil, WorkEventDiagnostics{}, err
		}
	}
	if filter.OperationID != "" && !validPrivateID(filter.OperationID) {
		return nil, WorkEventDiagnostics{}, errors.New("work event operation_id must be a 32-character lowercase hex identity")
	}
	paths, err := filepath.Glob(filepath.Join(store.Dir, "work-events-*.jsonl"))
	if err != nil {
		return nil, WorkEventDiagnostics{}, err
	}
	sort.Strings(paths)
	rows := make([]WorkEvent, 0)
	seen := map[string]struct{}{}
	now := time.Now
	if store.Now != nil {
		now = store.Now
	}
	maximumTimestamp := now().UTC().Add(maximumWorkClockSkew)
	for _, path := range paths {
		fileRows, readErr := readWorkEventRows(path, WorkEventFilter{})
		if readErr != nil {
			return nil, WorkEventDiagnostics{}, readErr
		}
		for _, event := range fileRows {
			if event.Timestamp.After(maximumTimestamp) {
				return nil, WorkEventDiagnostics{}, fmt.Errorf("work event %s timestamp %s is beyond allowed clock skew", event.EventID, event.Timestamp.Format(time.RFC3339Nano))
			}
			if _, duplicate := seen[event.EventID]; duplicate {
				return nil, WorkEventDiagnostics{}, fmt.Errorf("duplicate work event_id %s", event.EventID)
			}
			seen[event.EventID] = struct{}{}
			rows = append(rows, event)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Timestamp.Equal(rows[j].Timestamp) {
			if rows[i].OperationID == rows[j].OperationID {
				left, right := workEventOrder(rows[i].Event), workEventOrder(rows[j].Event)
				if left != right {
					return left < right
				}
			}
			return rows[i].EventID < rows[j].EventID
		}
		return rows[i].Timestamp.Before(rows[j].Timestamp)
	})
	failed, diagnostics := partitionWorkEventLifecycle(rows)
	filtered := make([]WorkEvent, 0, len(rows))
	for _, event := range rows {
		if _, bad := failed[event.OperationID]; bad {
			continue
		}
		if !filter.Since.IsZero() && event.Timestamp.Before(filter.Since) {
			continue
		}
		if filter.Class != "" && event.Class != filter.Class {
			continue
		}
		if filter.Event != "" && event.Event != filter.Event {
			continue
		}
		if filter.OperationID != "" && event.OperationID != filter.OperationID {
			continue
		}
		filtered = append(filtered, event)
	}
	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[len(filtered)-filter.Limit:]
	}
	return filtered, diagnostics, nil
}

// CountWrapperInterruptOperations streams only the day shards intersecting the
// requested window. The resident heartbeat needs one closed count, not the
// complete WorkEvent corpus and every calibration aggregate. Keeping this path
// streaming prevents 24-hour work history from becoming resident heap.
func (store *WorkEventStore) CountWrapperInterruptOperations(since, generatedAt time.Time) (count int, resultErr error) {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return 0, errors.New("work event store directory is required")
	}
	now := time.Now
	if store.Now != nil {
		now = store.Now
	}
	if generatedAt.IsZero() {
		generatedAt = now().UTC()
	} else {
		generatedAt = generatedAt.UTC()
	}
	if since.IsZero() || since.After(generatedAt) {
		return 0, errors.New("wrapper interrupt count requires a non-zero window start at or before generated_at")
	}
	since = since.UTC()
	maximumTimestamp := generatedAt.Add(maximumWorkClockSkew)
	startDay := since.In(time.Local).Format("20060102")
	endDay := maximumTimestamp.In(time.Local).Format("20060102")

	paths, err := filepath.Glob(filepath.Join(store.Dir, "work-events-*.jsonl"))
	if err != nil {
		return 0, err
	}
	sort.Strings(paths)
	seenEventIDs := make(map[string]struct{})
	interruptedOperations := make(map[string]struct{})
	failedOperations := make(map[string]struct{})
	lifecycle := newWorkEventLifecycleValidator()
	for _, path := range paths {
		base := filepath.Base(path)
		day := strings.TrimSuffix(strings.TrimPrefix(base, "work-events-"), ".jsonl")
		validDay := len(day) == len("20060102")
		for _, r := range day {
			if r < '0' || r > '9' {
				validDay = false
				break
			}
		}
		if validDay && (day < startDay || day > endDay) {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		line := 0
		for scanner.Scan() {
			line++
			var event WorkEvent
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				return 0, errors.Join(fmt.Errorf("decode %s line %d: %w", base, line, err), file.Close())
			}
			if err := event.Validate(); err != nil {
				return 0, errors.Join(fmt.Errorf("validate %s line %d: %w", base, line, err), file.Close())
			}
			if event.Timestamp.After(maximumTimestamp) {
				return 0, errors.Join(fmt.Errorf("work event %s timestamp %s is beyond allowed clock skew", event.EventID, event.Timestamp.Format(time.RFC3339Nano)), file.Close())
			}
			if _, duplicate := seenEventIDs[event.EventID]; duplicate {
				return 0, errors.Join(fmt.Errorf("duplicate work event_id %s", event.EventID), file.Close())
			}
			seenEventIDs[event.EventID] = struct{}{}
			if _, bad := failedOperations[event.OperationID]; bad {
				continue
			}
			if err := lifecycle.add(event); err != nil {
				// Tolerate a single unmodelled operation the same way the corpus
				// reader does; the resident heartbeat must not lose its whole
				// interrupt count to one bad lifecycle.
				failedOperations[event.OperationID] = struct{}{}
				delete(interruptedOperations, event.OperationID)
				continue
			}
			if event.Timestamp.Before(since) || !IsWrapperInterruptEvent(event) {
				continue
			}
			interruptedOperations[event.OperationID] = struct{}{}
		}
		resultErr = errors.Join(scanner.Err(), file.Close())
		if resultErr != nil {
			return 0, resultErr
		}
	}
	return len(interruptedOperations), nil
}

type workEventLifecycle struct {
	class         WorkClass
	weight        int
	leaseID       string
	commandDigest string
	sessionDigest string
	reserved      bool
	lastOrder     int
	seen          map[WorkEventType]struct{}
	terminal      WorkEventType
	terminalEvent WorkEvent
}

type workEventLifecycleValidator struct {
	operations map[string]*workEventLifecycle
}

func newWorkEventLifecycleValidator() *workEventLifecycleValidator {
	return &workEventLifecycleValidator{operations: make(map[string]*workEventLifecycle)}
}

func (validator *workEventLifecycleValidator) add(event WorkEvent) error {
	state := validator.operations[event.OperationID]
	if state == nil {
		state = &workEventLifecycle{class: event.Class, weight: event.Weight, lastOrder: -1, seen: make(map[WorkEventType]struct{})}
		validator.operations[event.OperationID] = state
	}
	if event.Class != state.class || event.Weight != state.weight {
		return fmt.Errorf("work operation %s changed class or weight", event.OperationID)
	}
	if _, duplicate := state.seen[event.Event]; duplicate && event.Event != WorkEventOverrideRequested && event.Event != WorkEventReserved && event.Event != WorkEventReacquired {
		return fmt.Errorf("work operation %s contains duplicate %s event", event.OperationID, event.Event)
	}
	state.seen[event.Event] = struct{}{}
	// An override is an operator annotation, not a lifecycle stage. A pressure
	// reservation returns an operation to the waiter set, where OverrideWaiter
	// still finds it, so override_requested is legitimately reachable after
	// acquired and reserved. Exempt it from the monotonic order check and leave
	// the cursor untouched; workEventOrder keeps its rank because it also breaks
	// sort ties between same-timestamp events.
	if event.Event == WorkEventOverrideRequested {
		if state.terminal != "" {
			return fmt.Errorf("work operation %s recorded %s after terminal %s", event.OperationID, event.Event, state.terminal)
		}
	} else {
		order := workEventOrder(event.Event)
		pressureCycle := event.Event == WorkEventReserved && state.lastOrder == workEventOrder(WorkEventReacquired) && !state.reserved
		if order < state.lastOrder && !pressureCycle {
			return fmt.Errorf("work operation %s lifecycle is out of order at %s", event.OperationID, event.Event)
		}
		state.lastOrder = order
	}
	if event.LeaseID != "" {
		if state.leaseID != "" && state.leaseID != event.LeaseID {
			if event.Event != WorkEventReacquired || !state.reserved {
				return fmt.Errorf("work operation %s changed lease identity", event.OperationID)
			}
		}
		state.leaseID = event.LeaseID
	}
	if event.Event == WorkEventReserved {
		if state.reserved {
			return fmt.Errorf("work operation %s reserved an already-reserved lease", event.OperationID)
		}
		state.reserved = true
	}
	if event.Event == WorkEventReacquired {
		if !state.reserved {
			return fmt.Errorf("work operation %s reacquired without a pressure reservation", event.OperationID)
		}
		state.reserved = false
	}
	if event.CommandDigest != "" {
		if state.commandDigest != "" && state.commandDigest != event.CommandDigest {
			return fmt.Errorf("work operation %s changed command digest", event.OperationID)
		}
		state.commandDigest = event.CommandDigest
	}
	if event.SessionDigest != "" {
		if state.sessionDigest != "" && state.sessionDigest != event.SessionDigest {
			return fmt.Errorf("work operation %s changed session digest", event.OperationID)
		}
		state.sessionDigest = event.SessionDigest
	}
	if isWorkTerminalEvent(event.Event) {
		if state.terminal != "" {
			if event.Event == WorkEventExpired && state.terminal != WorkEventExpired && isReconciliationExpiration(event.Outcome) {
				// Preserve and tolerate a historical cleanup receipt emitted after
				// the real child outcome. Consumers retain the first terminal event.
				return nil
			}
			if isLateRealTerminalAfterReconciliation(state.terminalEvent, event) {
				// A reconciler can observe the child exit in the narrow interval before
				// its wrapper persists the real outcome. Preserve both immutable rows,
				// but make the same-lease receipt the lifecycle authority.
				state.terminal = event.Event
				state.terminalEvent = event
				return nil
			}
			return fmt.Errorf("work operation %s contains multiple terminal events %s and %s", event.OperationID, state.terminal, event.Event)
		}
		state.terminal = event.Event
		state.terminalEvent = event
	}
	return nil
}

func validateWorkEventLifecycle(rows []WorkEvent) error {
	validator := newWorkEventLifecycleValidator()
	for _, event := range rows {
		if err := validator.add(event); err != nil {
			return err
		}
	}
	return nil
}

// WorkEventDiagnostics reports what a tolerant read had to drop. A telemetry
// reader must be more robust than its writer: one unmodelled operation must not
// blind every consumer for the whole retention window. Skips are surfaced, never
// silent, so a shrinking population is always attributable.
type WorkEventDiagnostics struct {
	SkippedOperations int      `json:"skipped_operations"`
	SkippedEvents     int      `json:"skipped_events"`
	Reasons           []string `json:"reasons,omitempty"`
}

// Degraded reports whether any operation was dropped from the read.
func (diagnostics WorkEventDiagnostics) Degraded() bool {
	return diagnostics.SkippedOperations > 0
}

// maximumWorkEventSkipReasons bounds the reason list so a systematically corrupt
// shard cannot turn a diagnostic field into an unbounded payload.
const maximumWorkEventSkipReasons = 8

// partitionWorkEventLifecycle validates every operation independently and returns
// the operation IDs that failed. Once an operation fails, its later events are
// not fed back into the validator, because its accumulated state is no longer
// trustworthy.
func partitionWorkEventLifecycle(rows []WorkEvent) (map[string]string, WorkEventDiagnostics) {
	validator := newWorkEventLifecycleValidator()
	failed := make(map[string]string)
	for _, event := range rows {
		if _, bad := failed[event.OperationID]; bad {
			continue
		}
		if err := validator.add(event); err != nil {
			failed[event.OperationID] = err.Error()
		}
	}
	if len(failed) == 0 {
		return failed, WorkEventDiagnostics{}
	}
	diagnostics := WorkEventDiagnostics{SkippedOperations: len(failed)}
	for _, event := range rows {
		if _, bad := failed[event.OperationID]; bad {
			diagnostics.SkippedEvents++
		}
	}
	reasons := make([]string, 0, len(failed))
	for _, reason := range failed {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	if len(reasons) > maximumWorkEventSkipReasons {
		reasons = reasons[:maximumWorkEventSkipReasons]
	}
	diagnostics.Reasons = reasons
	return failed, diagnostics
}

func isWorkTerminalEvent(event WorkEventType) bool {
	return event == WorkEventCompleted || event == WorkEventFailed || event == WorkEventCancelled || event == WorkEventExpired || event == WorkEventReused
}

func isReconciliationExpiration(outcome string) bool {
	switch outcome {
	case "dead_lease_owner", "reused_lease_pid", "dead_waiter_owner", "reused_waiter_pid":
		return true
	default:
		return false
	}
}

const maximumLateReconciliationTerminalLag = 5 * time.Second

func isLateRealTerminalAfterReconciliation(expired, terminal WorkEvent) bool {
	if expired.Event != WorkEventExpired || (expired.Outcome != "dead_lease_owner" && expired.Outcome != "reused_lease_pid") {
		return false
	}
	if terminal.Event != WorkEventCompleted && terminal.Event != WorkEventFailed && terminal.Event != WorkEventCancelled {
		return false
	}
	if terminal.ExitCode == nil || !validSHA256Digest(terminal.CommandDigest) {
		return false
	}
	if expired.OperationID != terminal.OperationID || expired.Class != terminal.Class || expired.Weight != terminal.Weight {
		return false
	}
	if expired.LeaseID == "" || expired.LeaseID != terminal.LeaseID || expired.PID <= 0 || expired.PID != terminal.PID {
		return false
	}
	lag := terminal.Timestamp.Sub(expired.Timestamp)
	return lag >= 0 && lag <= maximumLateReconciliationTerminalLag
}

func normalizeLateReconciliationTerminals(events []WorkEvent) ([]WorkEvent, int) {
	expiredByOperation := make(map[string]WorkEvent)
	recovered := make(map[string]struct{})
	for _, event := range events {
		if event.Event == WorkEventExpired {
			expiredByOperation[event.OperationID] = event
			continue
		}
		if expired, ok := expiredByOperation[event.OperationID]; ok && isLateRealTerminalAfterReconciliation(expired, event) {
			recovered[event.OperationID] = struct{}{}
		}
	}
	if len(recovered) == 0 {
		return events, 0
	}
	normalized := make([]WorkEvent, 0, len(events)-len(recovered))
	for _, event := range events {
		if event.Event == WorkEventExpired {
			if _, ok := recovered[event.OperationID]; ok {
				continue
			}
		}
		normalized = append(normalized, event)
	}
	return normalized, len(recovered)
}

func workEventOrder(eventType WorkEventType) int {
	switch eventType {
	case WorkEventQueued:
		return 0
	case WorkEventOverrideRequested:
		return 1
	case WorkEventAcquired:
		return 2
	case WorkEventReserved:
		return 3
	case WorkEventReacquired:
		return 4
	case WorkEventStarted:
		return 5
	case WorkEventCompleted, WorkEventFailed, WorkEventCancelled, WorkEventExpired, WorkEventReused:
		return 6
	default:
		return 6
	}
}

func readWorkEventRows(path string, filter WorkEventFilter) (rows []WorkEvent, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		var event WorkEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode work event %s line %d: %w", filepath.Base(path), line, err)
		}
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("validate work event %s line %d: %w", filepath.Base(path), line, err)
		}
		if !filter.Since.IsZero() && event.Timestamp.Before(filter.Since) {
			continue
		}
		if filter.Class != "" && event.Class != filter.Class {
			continue
		}
		if filter.Event != "" && event.Event != filter.Event {
			continue
		}
		if filter.OperationID != "" && event.OperationID != filter.OperationID {
			continue
		}
		rows = append(rows, event)
	}
	return rows, scanner.Err()
}

func (store *WorkEventStore) Prune(retentionDays int) error {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return errors.New("work event store directory is required")
	}
	if retentionDays < 1 {
		return errors.New("work event retention must be positive")
	}
	now := time.Now
	if store.Now != nil {
		now = store.Now
	}
	cutoff := now().AddDate(0, 0, -retentionDays)
	cutoffDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.Local)
	paths, err := filepath.Glob(filepath.Join(store.Dir, "work-events-*.jsonl"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		dateText := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "work-events-"), ".jsonl")
		day, parseErr := time.ParseInLocation("20060102", dateText, time.Local)
		if parseErr == nil && day.Before(cutoffDay) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

type WorkDurationStats struct {
	AverageMS int64 `json:"average_ms"`
	P95MS     int64 `json:"p95_ms"`
	MaximumMS int64 `json:"maximum_ms"`
}

type WorkRatioStats struct {
	Average float64 `json:"average"`
	P95     float64 `json:"p95"`
	Maximum float64 `json:"maximum"`
}

type WorkClassStats struct {
	Class           WorkClass         `json:"class"`
	Operations      int               `json:"operations"`
	Acquired        int               `json:"acquired"`
	Completed       int               `json:"completed"`
	Failed          int               `json:"failed"`
	Cancelled       int               `json:"cancelled"`
	Expired         int               `json:"expired"`
	Reused          int               `json:"reused"`
	Wait            WorkDurationStats `json:"wait"`
	Runtime         WorkDurationStats `json:"runtime"`
	WaitToRuntime   WorkRatioStats    `json:"wait_to_runtime"`
	BoundedSlowdown WorkRatioStats    `json:"bounded_slowdown"`
}

type WorkReviewSignals struct {
	CPUOnlyDeferrals                   int `json:"cpu_only_deferrals"`
	CPUDeferralsWithCoordinatedWork    int `json:"cpu_deferrals_with_coordinated_work"`
	CPUDeferralsWithoutCoordinatedWork int `json:"cpu_deferrals_without_coordinated_work"`
	CPUDeferralsAttributionUnavailable int `json:"cpu_deferrals_attribution_unavailable"`
	MemoryDeferrals                    int `json:"memory_deferrals"`
	StorageDeferrals                   int `json:"storage_deferrals"`
	LongWaitOperations                 int `json:"long_wait_operations"`
	IncompleteOperations               int `json:"incomplete_operations"`
	ExpiredOwnerEvents                 int `json:"expired_owner_events"`
	ReconciliationRaceRecoveries       int `json:"reconciliation_race_recoveries"`
	CancelledOperations                int `json:"cancelled_operations"`
	// WrapperInterruptOperations counts cancelled work with a closed interrupt
	// outcome/reason (agent-killed wrappers). Privacy-safe; never argv-based.
	WrapperInterruptOperations int `json:"wrapper_interrupt_operations"`
	BypassedAdmissions         int `json:"bypassed_admissions"`
	ProtectedAcquisitions      int `json:"protected_acquisitions"`
	ReservationDeferrals       int `json:"reservation_deferrals"`
	CacheHits                  int `json:"cache_hits"`
	CacheMisses                int `json:"cache_misses"`
	SingleflightWaits          int `json:"singleflight_waits"`
	OperatorOverrides          int `json:"operator_overrides"`
	AgentPriorityRequests      int `json:"agent_priority_requests"`
	ClassReclassifications     int `json:"class_reclassifications"`
	FullToExpressAdjustments   int `json:"full_to_express_adjustments"`
	ExpressToFullAdjustments   int `json:"express_to_full_adjustments"`
	WarningCapacityAdmissions  int `json:"warning_capacity_admissions"`
	WarningCapacityDeferrals   int `json:"warning_capacity_deferrals"`
}

// Closed outcomes that classify a cancelled operation as wrapper/agent interrupt.
var wrapperInterruptOutcomes = map[string]struct{}{
	"wrapper_interrupt": {},
	"agent_kill":        {},
	"signal_interrupt":  {},
	"interrupted":       {},
	// Legacy outcome before closed enum ship; still counts for forensics.
	"signal_forwarded": {},
}

// IsWrapperInterruptEvent reports whether a cancelled work event is a closed
// wrapper/agent interrupt (storage-red P5 forensics). Fail closed on ambiguity.
func IsWrapperInterruptEvent(event WorkEvent) bool {
	if event.Event != WorkEventCancelled {
		return false
	}
	outcome := strings.ToLower(strings.TrimSpace(event.Outcome))
	if _, ok := wrapperInterruptOutcomes[outcome]; ok {
		return true
	}
	// Closed tokens only — whole-string equality or exact reason field tokens.
	for _, field := range []string{event.DecisionReason, event.PressureReason, event.Outcome} {
		token := strings.ToLower(strings.TrimSpace(field))
		if _, ok := wrapperInterruptOutcomes[token]; ok {
			return true
		}
	}
	return false
}

const (
	WorkBoundedSlowdownP95Target  = 5.0
	WorkP95MinimumSamples         = 20
	WorkSlowdownSLOScope          = "end-to-end"
	WorkPressureConditionedScope  = "host-pressure-excluded"
	WorkPressureConditionedMethod = "subtract-observed-host-pressure-wait-v1"
	// Terminal-runtime samples include completed, failed, and cancelled work
	// with a positive observed runtime. Synthetic owner expiry and zero-runtime
	// cancellation cannot produce a meaningful slowdown denominator.
	WorkSlowdownSLOSampleScope = "terminal-runtime"
)

type WorkClassSLOBreach struct {
	Class       WorkClass `json:"class"`
	ObservedP95 float64   `json:"observed_p95"`
}

type WorkClassSLOSample struct {
	Class   WorkClass `json:"class"`
	Samples int       `json:"samples"`
}

type WorkServiceLevel struct {
	Status                   string               `json:"status"`
	Scope                    string               `json:"scope"`
	SampleScope              string               `json:"sample_scope"`
	TargetP95BoundedSlowdown float64              `json:"target_p95_bounded_slowdown"`
	SamplesRequired          int                  `json:"samples_required"`
	EvaluatedClasses         int                  `json:"evaluated_classes"`
	EvaluatedSamples         []WorkClassSLOSample `json:"evaluated_samples"`
	DeferredClasses          []WorkClassSLOSample `json:"deferred_classes"`
	Breaches                 []WorkClassSLOBreach `json:"breaches"`
}

// WorkPressureConditionedClass is a supplemental per-class view that removes
// only measured host-pressure wait from the end-to-end wait. It is diagnostic
// evidence, never an authority that can replace or promote the end-to-end SLO.
type WorkPressureConditionedClass struct {
	Class                   WorkClass `json:"class"`
	Samples                 int       `json:"samples"`
	PressureAffectedSamples int       `json:"pressure_affected_samples"`
	ExcludedWaitMS          int64     `json:"excluded_wait_ms"`
	P95BoundedSlowdown      float64   `json:"p95_bounded_slowdown"`
	Status                  string    `json:"status"`
}

// WorkPressureConditionedServiceLevel separates scheduler-controllable delay
// from observed host-pressure delay without hiding end-to-end pain. Evidence
// is accepted only for schema-v4 terminal-runtime lifecycles whose queued row
// is inside the requested window and whose decomposition reconciles.
type WorkPressureConditionedServiceLevel struct {
	SchemaVersion               int                            `json:"schema_version"`
	Status                      string                         `json:"status"`
	EvidenceStatus              string                         `json:"evidence_status"`
	Scope                       string                         `json:"scope"`
	SampleScope                 string                         `json:"sample_scope"`
	Method                      string                         `json:"method"`
	InformationalOnly           bool                           `json:"informational_only"`
	EndToEndAuthoritative       bool                           `json:"end_to_end_authoritative"`
	TargetP95BoundedSlowdown    float64                        `json:"target_p95_bounded_slowdown"`
	SamplesRequired             int                            `json:"samples_required"`
	TerminalRuntimeSamples      int                            `json:"terminal_runtime_samples"`
	EligibleSamples             int                            `json:"eligible_samples"`
	WindowBoundarySamples       int                            `json:"window_boundary_samples"`
	LegacySchemaSamples         int                            `json:"legacy_schema_samples"`
	InvalidDecompositionSamples int                            `json:"invalid_decomposition_samples"`
	PressureAffectedSamples     int                            `json:"pressure_affected_samples"`
	ExcludedWaitMS              int64                          `json:"excluded_wait_ms"`
	EvaluatedClasses            int                            `json:"evaluated_classes"`
	EvaluatedSamples            []WorkClassSLOSample           `json:"evaluated_samples"`
	DeferredClasses             []WorkClassSLOSample           `json:"deferred_classes"`
	Breaches                    []WorkClassSLOBreach           `json:"breaches"`
	ByClass                     []WorkPressureConditionedClass `json:"by_class"`
}

// WorkCalibrationCohort keeps rolling slowdown evidence from unlike selector
// and weight revisions out of the same p95. Current marks the most recently
// observed terminal-runtime cohort for a class; it is not a policy authority.
type WorkCalibrationCohort struct {
	Class                  WorkClass `json:"class"`
	Weight                 int       `json:"weight"`
	SchedulingPolicy       string    `json:"scheduling_policy,omitempty"`
	SelectorSchemaVersion  int       `json:"selector_schema_version"`
	TerminalRuntimeSamples int       `json:"terminal_runtime_samples"`
	P95BoundedSlowdown     float64   `json:"p95_bounded_slowdown"`
	Status                 string    `json:"status"`
	Current                bool      `json:"current"`
}

type WorkStats struct {
	SchemaVersion                   int                                 `json:"schema_version"`
	Since                           time.Time                           `json:"since"`
	GeneratedAt                     time.Time                           `json:"generated_at"`
	EventCount                      int                                 `json:"event_count"`
	OperationCount                  int                                 `json:"operation_count"`
	OpenOperations                  int                                 `json:"open_operations"`
	ByClass                         []WorkClassStats                    `json:"by_class"`
	ReviewSignals                   WorkReviewSignals                   `json:"review_signals"`
	ServiceLevel                    WorkServiceLevel                    `json:"service_level"`
	PressureConditionedServiceLevel WorkPressureConditionedServiceLevel `json:"pressure_conditioned_service_level"`
	CalibrationCohorts              []WorkCalibrationCohort             `json:"calibration_cohorts"`
}

func SummarizeWorkEvents(events []WorkEvent, since, generatedAt time.Time) WorkStats {
	rawEventCount := len(events)
	events, reconciliationRaceRecoveries := normalizeLateReconciliationTerminals(events)
	classes := AllWorkClasses()
	byClass := make(map[WorkClass]*WorkClassStats, len(classes))
	waits := make(map[WorkClass][]int64, len(classes))
	runtimes := make(map[WorkClass][]int64, len(classes))
	waitToRuntime := make(map[WorkClass][]float64, len(classes))
	boundedSlowdown := make(map[WorkClass][]float64, len(classes))
	conditionedSlowdown := make(map[WorkClass][]float64, len(classes))
	conditionedAffected := make(map[WorkClass]int, len(classes))
	conditionedExcludedWait := make(map[WorkClass]int64, len(classes))
	type cohortKey struct {
		class            WorkClass
		weight           int
		schedulingPolicy string
		selectorSchema   int
	}
	cohortSlowdown := make(map[cohortKey][]float64)
	currentCohort := make(map[WorkClass]cohortKey)
	for _, class := range classes {
		byClass[class] = &WorkClassStats{Class: class}
	}
	type operationState struct {
		class                  WorkClass
		terminal               bool
		pressureSignal         bool
		longWaitSignal         bool
		cancelledSignal        bool
		wrapperInterruptSignal bool
		expiredOwnerSeen       bool
		cacheSignal            bool
		singleflightSeen       bool
		cpuAttribution         string
		leaseWaitMS            int64
		waitRecorded           bool
		waitMS                 int64
		queuedObserved         bool
		initialPressureSignal  bool
		warningDecisionSeen    bool
	}
	operations := map[string]operationState{}
	signals := WorkReviewSignals{
		ExpiredOwnerEvents:           reconciliationRaceRecoveries,
		ReconciliationRaceRecoveries: reconciliationRaceRecoveries,
	}
	conditioned := WorkPressureConditionedServiceLevel{
		SchemaVersion: 1, Status: "insufficient-data", EvidenceStatus: "complete", Scope: WorkPressureConditionedScope,
		SampleScope: WorkSlowdownSLOSampleScope, Method: WorkPressureConditionedMethod,
		InformationalOnly: true, EndToEndAuthoritative: true,
		TargetP95BoundedSlowdown: WorkBoundedSlowdownP95Target, SamplesRequired: WorkP95MinimumSamples,
		EvaluatedSamples: []WorkClassSLOSample{}, DeferredClasses: []WorkClassSLOSample{},
		Breaches: []WorkClassSLOBreach{}, ByClass: []WorkPressureConditionedClass{},
	}
	classifyCPUAttribution := func(event WorkEvent) string {
		if !event.CoordinatedWorkAttributionAvailable || !event.CoordinatedWorkCPUAvailable {
			return "unavailable"
		}
		if event.CoordinatedWorkCPUPercent > 0 {
			return "active"
		}
		return "idle"
	}
	addCPUAttribution := func(classification string, delta int) {
		switch classification {
		case "active":
			signals.CPUDeferralsWithCoordinatedWork += delta
		case "idle":
			signals.CPUDeferralsWithoutCoordinatedWork += delta
		default:
			signals.CPUDeferralsAttributionUnavailable += delta
		}
	}
	recordCohort := func(event WorkEvent, waitMS, runtimeMS int64) {
		if runtimeMS <= 0 {
			return
		}
		key := cohortKey{
			class: event.Class, weight: event.Weight, schedulingPolicy: event.SchedulingPolicy,
			selectorSchema: event.SelectorSchemaVersion,
		}
		cohortSlowdown[key] = append(cohortSlowdown[key], workBoundedSlowdown(waitMS, runtimeMS))
		currentCohort[event.Class] = key
	}
	recordPressureConditioned := func(event WorkEvent, state operationState, waitMS, runtimeMS int64) {
		if runtimeMS <= 0 {
			return
		}
		conditioned.TerminalRuntimeSamples++
		if !state.queuedObserved {
			conditioned.WindowBoundarySamples++
			return
		}
		if event.SchemaVersion < WorkEventSchemaVersion {
			conditioned.LegacySchemaSamples++
			return
		}
		excludedWaitMS := event.PressureWaitMilliseconds
		if state.initialPressureSignal {
			excludedWaitMS += event.AdmissionWaitMilliseconds
		}
		if excludedWaitMS < 0 || excludedWaitMS > waitMS {
			conditioned.InvalidDecompositionSamples++
			return
		}
		conditionedWaitMS := waitMS - excludedWaitMS
		conditionedSlowdown[event.Class] = append(conditionedSlowdown[event.Class], workBoundedSlowdown(conditionedWaitMS, runtimeMS))
		conditioned.EligibleSamples++
		if excludedWaitMS > 0 {
			conditioned.PressureAffectedSamples++
			conditioned.ExcludedWaitMS += excludedWaitMS
			conditionedAffected[event.Class]++
			conditionedExcludedWait[event.Class] += excludedWaitMS
		}
	}
	recordWait := func(event WorkEvent, state *operationState) {
		if state.waitRecorded {
			return
		}
		waitMS := event.WaitMilliseconds
		if waitMS == 0 {
			waitMS = state.leaseWaitMS
		}
		waits[event.Class] = append(waits[event.Class], waitMS)
		state.waitRecorded = true
		state.waitMS = waitMS
		if waitMS >= int64((2*time.Minute)/time.Millisecond) && !state.longWaitSignal {
			signals.LongWaitOperations++
			state.longWaitSignal = true
		}
	}
	for _, event := range events {
		state, known := operations[event.OperationID]
		if !known {
			if stats := byClass[event.Class]; stats != nil {
				stats.Operations++
			}
		}
		state.class = event.Class
		if (event.Event == WorkEventQueued || event.Event == WorkEventReused) && event.RequestedClass != "" {
			signals.ClassReclassifications++
			if (event.RequestedClass == WorkClassTest && event.Class == WorkClassExpressTest) || (event.RequestedClass == WorkClassBuild && event.Class == WorkClassExpressBuild) {
				signals.FullToExpressAdjustments++
			}
			if (event.RequestedClass == WorkClassExpressTest && event.Class == WorkClassTest) || (event.RequestedClass == WorkClassExpressBuild && event.Class == WorkClassBuild) {
				signals.ExpressToFullAdjustments++
			}
		}
		if event.Event == WorkEventQueued {
			state.queuedObserved = true
			if event.PressureDimension != "" {
				state.initialPressureSignal = true
			}
		}
		if !state.warningDecisionSeen {
			switch event.AdmissionDecision {
			case WarningCapacityAdmittedDecision:
				signals.WarningCapacityAdmissions++
				state.warningDecisionSeen = true
			case WarningCapacityDeferredDecision:
				signals.WarningCapacityDeferrals++
				state.warningDecisionSeen = true
			}
		}
		stats := byClass[event.Class]
		if stats == nil {
			stats = &WorkClassStats{Class: event.Class}
			byClass[event.Class] = stats
		}
		if !state.pressureSignal {
			if event.PressureDimension == "cpu" {
				signals.CPUOnlyDeferrals++
				state.cpuAttribution = classifyCPUAttribution(event)
				addCPUAttribution(state.cpuAttribution, 1)
				state.pressureSignal = true
			} else if event.PressureDimension == "storage" {
				signals.StorageDeferrals++
				state.pressureSignal = true
			} else if event.PressureDimension != "" {
				signals.MemoryDeferrals++
				state.pressureSignal = true
			}
		} else if event.PressureDimension == "cpu" && state.cpuAttribution == "unavailable" {
			// A queued event can race the first resident attribution refresh. Keep
			// the operation count stable but accept better evidence from its later
			// acquired receipt instead of freezing a transient unavailable result.
			classification := classifyCPUAttribution(event)
			if classification != "unavailable" {
				addCPUAttribution(state.cpuAttribution, -1)
				addCPUAttribution(classification, 1)
				state.cpuAttribution = classification
			}
		}
		if event.BypassedCount > 0 {
			signals.BypassedAdmissions += event.BypassedCount
		}
		if event.Event == WorkEventAcquired && event.ProtectedOperationID == event.OperationID {
			signals.ProtectedAcquisitions++
		}
		if event.Event == WorkEventOverrideRequested {
			if event.Outcome == "agent_priority_requested" {
				signals.AgentPriorityRequests++
			} else {
				signals.OperatorOverrides++
			}
		}
		if event.Blocker == WorkBlockerReservation {
			signals.ReservationDeferrals++
		}
		if !state.cacheSignal && event.ReuseStatus != "" {
			if event.ReuseStatus == "hit" {
				signals.CacheHits++
			}
			if event.ReuseStatus == "miss" {
				signals.CacheMisses++
			}
			state.cacheSignal = true
		}
		if !state.singleflightSeen && event.SingleflightWaitMS > 0 {
			signals.SingleflightWaits++
			state.singleflightSeen = true
		}
		switch event.Event {
		case WorkEventAcquired:
			stats.Acquired++
			state.leaseWaitMS = event.WaitMilliseconds
		case WorkEventReacquired:
			state.leaseWaitMS = event.WaitMilliseconds
		case WorkEventStarted:
			waitMS := event.WaitMilliseconds
			if waitMS == 0 {
				waitMS = state.leaseWaitMS
			}
			waits[event.Class] = append(waits[event.Class], waitMS)
			state.waitRecorded = true
			state.waitMS = waitMS
			if waitMS >= int64((2*time.Minute)/time.Millisecond) && !state.longWaitSignal {
				signals.LongWaitOperations++
				state.longWaitSignal = true
			}
		case WorkEventCompleted:
			if !state.terminal {
				recordWait(event, &state)
				stats.Completed++
				state.terminal = true
				runtimes[event.Class] = append(runtimes[event.Class], event.RuntimeMillis)
				recordWorkRatios(waitToRuntime, boundedSlowdown, event.Class, state.waitMS, event.RuntimeMillis)
				recordCohort(event, state.waitMS, event.RuntimeMillis)
				recordPressureConditioned(event, state, state.waitMS, event.RuntimeMillis)
			}
		case WorkEventFailed:
			if !state.terminal {
				recordWait(event, &state)
				stats.Failed++
				state.terminal = true
				runtimes[event.Class] = append(runtimes[event.Class], event.RuntimeMillis)
				recordWorkRatios(waitToRuntime, boundedSlowdown, event.Class, state.waitMS, event.RuntimeMillis)
				recordCohort(event, state.waitMS, event.RuntimeMillis)
				recordPressureConditioned(event, state, state.waitMS, event.RuntimeMillis)
			}
		case WorkEventCancelled:
			if !state.terminal {
				recordWait(event, &state)
				stats.Cancelled++
				if !state.cancelledSignal {
					signals.CancelledOperations++
					state.cancelledSignal = true
				}
				if !state.wrapperInterruptSignal && IsWrapperInterruptEvent(event) {
					signals.WrapperInterruptOperations++
					state.wrapperInterruptSignal = true
				}
				state.terminal = true
				runtimes[event.Class] = append(runtimes[event.Class], event.RuntimeMillis)
				recordWorkRatios(waitToRuntime, boundedSlowdown, event.Class, state.waitMS, event.RuntimeMillis)
				recordCohort(event, state.waitMS, event.RuntimeMillis)
				recordPressureConditioned(event, state, state.waitMS, event.RuntimeMillis)
			}
		case WorkEventExpired:
			if !state.expiredOwnerSeen {
				signals.ExpiredOwnerEvents++
				state.expiredOwnerSeen = true
			}
			if !state.terminal {
				recordWait(event, &state)
				stats.Expired++
				state.terminal = true
			}
		case WorkEventReused:
			if !state.terminal {
				stats.Reused++
				state.terminal = true
			}
		}
		operations[event.OperationID] = state
	}
	resultClasses := make([]WorkClassStats, 0, len(classes))
	serviceLevel := WorkServiceLevel{
		Status: "insufficient-data", Scope: WorkSlowdownSLOScope, SampleScope: WorkSlowdownSLOSampleScope,
		TargetP95BoundedSlowdown: WorkBoundedSlowdownP95Target, SamplesRequired: WorkP95MinimumSamples,
		EvaluatedSamples: []WorkClassSLOSample{}, DeferredClasses: []WorkClassSLOSample{}, Breaches: []WorkClassSLOBreach{},
	}
	for _, class := range classes {
		stats := byClass[class]
		stats.Wait = durationStats(waits[class])
		stats.Runtime = durationStats(runtimes[class])
		stats.WaitToRuntime = ratioStats(waitToRuntime[class])
		stats.BoundedSlowdown = ratioStats(boundedSlowdown[class])
		samples := len(boundedSlowdown[class])
		if samples >= WorkP95MinimumSamples {
			serviceLevel.EvaluatedClasses++
			serviceLevel.EvaluatedSamples = append(serviceLevel.EvaluatedSamples, WorkClassSLOSample{Class: class, Samples: samples})
			if stats.BoundedSlowdown.P95 > WorkBoundedSlowdownP95Target {
				serviceLevel.Breaches = append(serviceLevel.Breaches, WorkClassSLOBreach{Class: class, ObservedP95: stats.BoundedSlowdown.P95})
			}
		} else if samples > 0 {
			serviceLevel.DeferredClasses = append(serviceLevel.DeferredClasses, WorkClassSLOSample{Class: class, Samples: samples})
		}
		if stats.Operations == 0 {
			if len(conditionedSlowdown[class]) == 0 {
				continue
			}
		}
		if stats.Operations > 0 {
			resultClasses = append(resultClasses, *stats)
		}
		conditionedValues := conditionedSlowdown[class]
		if len(conditionedValues) > 0 {
			p95 := ratioStats(conditionedValues).P95
			status := "deferred"
			if len(conditionedValues) >= WorkP95MinimumSamples {
				conditioned.EvaluatedClasses++
				conditioned.EvaluatedSamples = append(conditioned.EvaluatedSamples, WorkClassSLOSample{Class: class, Samples: len(conditionedValues)})
				status = "met"
				if p95 > WorkBoundedSlowdownP95Target {
					status = "breached"
					conditioned.Breaches = append(conditioned.Breaches, WorkClassSLOBreach{Class: class, ObservedP95: p95})
				}
			} else {
				conditioned.DeferredClasses = append(conditioned.DeferredClasses, WorkClassSLOSample{Class: class, Samples: len(conditionedValues)})
			}
			conditioned.ByClass = append(conditioned.ByClass, WorkPressureConditionedClass{
				Class: class, Samples: len(conditionedValues), PressureAffectedSamples: conditionedAffected[class],
				ExcludedWaitMS: conditionedExcludedWait[class], P95BoundedSlowdown: p95, Status: status,
			})
		}
	}
	if serviceLevel.EvaluatedClasses > 0 {
		serviceLevel.Status = "met"
		if len(serviceLevel.Breaches) > 0 {
			serviceLevel.Status = "breached"
		}
	}
	if conditioned.EvaluatedClasses > 0 {
		conditioned.Status = "met"
		if len(conditioned.Breaches) > 0 {
			conditioned.Status = "breached"
		}
	}
	if conditioned.InvalidDecompositionSamples > 0 {
		conditioned.EvidenceStatus = "invalid"
	} else if conditioned.WindowBoundarySamples+conditioned.LegacySchemaSamples > 0 {
		conditioned.EvidenceStatus = "partial"
	}
	open := 0
	for _, state := range operations {
		if !state.terminal {
			open++
		}
	}
	signals.IncompleteOperations = open
	cohorts := make([]WorkCalibrationCohort, 0, len(cohortSlowdown))
	for key, values := range cohortSlowdown {
		p95 := ratioStats(values).P95
		status := "deferred"
		if len(values) >= WorkP95MinimumSamples {
			status = "met"
			if p95 > WorkBoundedSlowdownP95Target {
				status = "breached"
			}
		}
		cohorts = append(cohorts, WorkCalibrationCohort{
			Class: key.class, Weight: key.weight, SchedulingPolicy: key.schedulingPolicy,
			SelectorSchemaVersion: key.selectorSchema, TerminalRuntimeSamples: len(values),
			P95BoundedSlowdown: p95, Status: status, Current: currentCohort[key.class] == key,
		})
	}
	classRank := make(map[WorkClass]int, len(classes))
	for index, class := range classes {
		classRank[class] = index
	}
	sort.Slice(cohorts, func(i, j int) bool {
		left, right := cohorts[i], cohorts[j]
		if classRank[left.Class] != classRank[right.Class] {
			return classRank[left.Class] < classRank[right.Class]
		}
		if left.Current != right.Current {
			return left.Current
		}
		if left.SelectorSchemaVersion != right.SelectorSchemaVersion {
			return left.SelectorSchemaVersion > right.SelectorSchemaVersion
		}
		if left.Weight != right.Weight {
			return left.Weight < right.Weight
		}
		return left.SchedulingPolicy < right.SchedulingPolicy
	})
	return WorkStats{
		SchemaVersion: WorkEventSchemaVersion,
		Since:         since.UTC(), GeneratedAt: generatedAt.UTC(), EventCount: rawEventCount,
		OperationCount: len(operations), OpenOperations: open, ByClass: resultClasses,
		ReviewSignals: signals, ServiceLevel: serviceLevel,
		PressureConditionedServiceLevel: conditioned, CalibrationCohorts: cohorts,
	}
}

func recordWorkRatios(waitToRuntime, boundedSlowdown map[WorkClass][]float64, class WorkClass, waitMS, runtimeMS int64) {
	if runtimeMS <= 0 {
		return
	}
	waitToRuntime[class] = append(waitToRuntime[class], float64(waitMS)/float64(runtimeMS))
	boundedSlowdown[class] = append(boundedSlowdown[class], workBoundedSlowdown(waitMS, runtimeMS))
}

func workBoundedSlowdown(waitMS, runtimeMS int64) float64 {
	if runtimeMS <= 0 {
		return 0
	}
	denominator := max(float64(runtimeMS), float64((10*time.Second)/time.Millisecond))
	return float64(waitMS+runtimeMS) / denominator
}

func ratioStats(values []float64) WorkRatioStats {
	if len(values) == 0 {
		return WorkRatioStats{}
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	var total float64
	for _, value := range ordered {
		total += value
	}
	p95 := (len(ordered)*95 + 99) / 100
	return WorkRatioStats{Average: total / float64(len(ordered)), P95: ordered[p95-1], Maximum: ordered[len(ordered)-1]}
}

func durationStats(values []int64) WorkDurationStats {
	if len(values) == 0 {
		return WorkDurationStats{}
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	var total int64
	for _, value := range ordered {
		total += value
	}
	p95 := (len(ordered)*95 + 99) / 100
	return WorkDurationStats{AverageMS: total / int64(len(ordered)), P95MS: ordered[p95-1], MaximumMS: ordered[len(ordered)-1]}
}
