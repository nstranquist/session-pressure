package sessionpressure

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	workTestOperation = "00000000000000000000000000000001"
	workTestLease     = "00000000000000000000000000000002"
)

func validWorkEvent(at time.Time, eventType WorkEventType) WorkEvent {
	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		Timestamp:     at,
		Event:         eventType,
		OperationID:   workTestOperation,
		Class:         WorkClassTest,
		Weight:        2,
		CommandDigest: CommandShapeDigest("/usr/bin/go", 3),
	}
	if eventType == WorkEventAcquired || eventType == WorkEventStarted || eventType == WorkEventCompleted {
		event.LeaseID = workTestLease
	}
	return event
}

func TestWorkEventRejectsInvalidReuseKeyDigest(t *testing.T) {
	event := validWorkEvent(time.Now().UTC(), WorkEventQueued)
	event.EventID = workEventID(event.OperationID, event.Event, event.LeaseID)
	event.ReuseKeyDigest = "not-a-digest"
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "reuse_key_digest") {
		t.Fatalf("invalid reuse key digest unexpectedly accepted: %v", err)
	}
}

func TestWorkEventValidatesRequestedClassAuditField(t *testing.T) {
	event := validWorkEvent(time.Now().UTC(), WorkEventQueued)
	event.EventID = workEventID(event.OperationID, event.Event, event.LeaseID)
	event.RequestedClass = WorkClass("not-a-class")
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "requested_class") {
		t.Fatalf("invalid requested class unexpectedly accepted: %v", err)
	}
	event.RequestedClass = event.Class
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "must be omitted") {
		t.Fatalf("redundant requested class unexpectedly accepted: %v", err)
	}
	event.RequestedClass = WorkClassBuild
	if err := event.Validate(); err != nil {
		t.Fatalf("distinct requested class rejected: %v", err)
	}
}

func TestWorkEventStoreIsDurableIdempotentAndLifecycleOrdered(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	store := NewWorkEventStore(dir)
	store.Now = func() time.Time { return now }

	// Deliberately append in the wrong order at the same timestamp. Reads use
	// lifecycle order, not filesystem or hash luck.
	for _, eventType := range []WorkEventType{WorkEventCompleted, WorkEventQueued, WorkEventStarted, WorkEventAcquired, WorkEventQueued} {
		event := validWorkEvent(now, eventType)
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.Read(WorkEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []WorkEventType{WorkEventQueued, WorkEventAcquired, WorkEventStarted, WorkEventCompleted}
	if len(events) != len(want) {
		t.Fatalf("events=%+v", events)
	}
	for index, eventType := range want {
		if events[index].Event != eventType {
			t.Fatalf("event order=%+v", events)
		}
	}
	info, err := os.Stat(store.dayPath(now))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("work ledger mode=%o", info.Mode().Perm())
	}
}

// TestWorkEventStoreFiltersByOperationID keeps a single-operation inspector from
// having to read the whole window and discard nearly all of it.
func TestWorkEventStoreFiltersByOperationID(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	store := NewWorkEventStore(dir)
	store.Now = func() time.Time { return now }

	other := "0000000000000000000000000000beef"
	for _, eventType := range []WorkEventType{WorkEventQueued, WorkEventAcquired, WorkEventCompleted} {
		if err := store.AppendDurable(validWorkEvent(now, eventType)); err != nil {
			t.Fatal(err)
		}
		event := validWorkEvent(now, eventType)
		event.OperationID = other
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}

	all, err := store.Read(WorkEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Fatalf("unfiltered events=%d, want both operations", len(all))
	}
	scoped, err := store.Read(WorkEventFilter{OperationID: other})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 3 {
		t.Fatalf("scoped events=%d: %+v", len(scoped), scoped)
	}
	for _, event := range scoped {
		if event.OperationID != other {
			t.Fatalf("filter leaked another operation: %+v", event)
		}
	}
	// A malformed identity is rejected outright rather than silently matching
	// nothing, which would read as "this operation has no history".
	if _, err := store.Read(WorkEventFilter{OperationID: "not-an-identity"}); err == nil {
		t.Fatal("malformed operation_id filter must be rejected")
	}
}

func TestWorkEventStoreSerializesConcurrentIdenticalAppends(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	store := NewWorkEventStore(dir)
	event := validWorkEvent(now, WorkEventQueued)
	var group sync.WaitGroup
	errors := make(chan error, 16)
	for index := 0; index < cap(errors); index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			errors <- store.AppendDurable(event)
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.Read(WorkEventFilter{})
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

// TestWorkEventLifecycleAllowsOperatorOverrideAfterPressureReservation replays the
// exact 2026-07-25 sequence that made every reader abort. A pressure reservation
// returns an operation to the waiter set, so the operator can still promote it
// after acquire and reserve; the override is an annotation, not a stage.
func TestWorkEventLifecycleAllowsOperatorOverrideAfterPressureReservation(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	store := NewWorkEventStore(dir)
	store.Now = func() time.Time { return now.Add(time.Hour) }

	firstLease := "0000000000000000000000000000000a"
	secondLease := "0000000000000000000000000000000b"
	steps := []struct {
		event   WorkEventType
		leaseID string
		offset  time.Duration
	}{
		{WorkEventQueued, "", 0},
		{WorkEventAcquired, firstLease, 1 * time.Minute},
		{WorkEventReserved, firstLease, 2 * time.Minute},
		{WorkEventOverrideRequested, "", 3 * time.Minute},
		{WorkEventReacquired, secondLease, 4 * time.Minute},
		{WorkEventStarted, secondLease, 5 * time.Minute},
		{WorkEventCompleted, secondLease, 6 * time.Minute},
	}
	for _, step := range steps {
		event := validWorkEvent(now.Add(step.offset), step.event)
		event.LeaseID = step.leaseID
		if err := store.AppendDurable(event); err != nil {
			t.Fatalf("append %s: %v", step.event, err)
		}
	}

	events, diagnostics, err := store.ReadWithDiagnostics(WorkEventFilter{})
	if err != nil {
		t.Fatalf("override after pressure reservation must remain readable: %v", err)
	}
	if diagnostics.Degraded() {
		t.Fatalf("legitimate override was skipped: %+v", diagnostics)
	}
	if len(events) != len(steps) {
		t.Fatalf("events=%d want=%d", len(events), len(steps))
	}
	for index, step := range steps {
		if events[index].Event != step.event {
			t.Fatalf("event order=%+v", events)
		}
	}
}

// TestWorkEventLifecycleRejectsOverrideAfterTerminal keeps the annotation bounded:
// promoting an operation that already finished is still nonsense.
func TestWorkEventLifecycleRejectsOverrideAfterTerminal(t *testing.T) {
	now := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	rows := []WorkEvent{
		validWorkEvent(now, WorkEventQueued),
		validWorkEvent(now.Add(time.Minute), WorkEventCompleted),
		validWorkEvent(now.Add(2*time.Minute), WorkEventOverrideRequested),
	}
	err := validateWorkEventLifecycle(rows)
	if err == nil || !strings.Contains(err.Error(), "after terminal") {
		t.Fatalf("override after terminal unexpectedly accepted: %v", err)
	}
}

// TestWorkEventStoreSkipsOneBadOperationAndReportsIt proves the reader is more
// robust than its writer: one unmodelled operation must not blind the corpus.
func TestWorkEventStoreSkipsOneBadOperationAndReportsIt(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	store := NewWorkEventStore(dir)
	store.Now = func() time.Time { return now.Add(time.Hour) }

	healthy := []WorkEventType{WorkEventQueued, WorkEventAcquired, WorkEventStarted, WorkEventCompleted}
	for index, eventType := range healthy {
		event := validWorkEvent(now.Add(time.Duration(index)*time.Minute), eventType)
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}

	// A second operation reacquires without ever holding a pressure reservation.
	badOperation := "000000000000000000000000000000ff"
	for index, eventType := range []WorkEventType{WorkEventQueued, WorkEventReacquired} {
		event := validWorkEvent(now.Add(time.Duration(index)*time.Minute), eventType)
		event.OperationID = badOperation
		if eventType == WorkEventReacquired {
			event.LeaseID = workTestLease
		}
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}

	events, diagnostics, err := store.ReadWithDiagnostics(WorkEventFilter{})
	if err != nil {
		t.Fatalf("one bad operation must not fail the read: %v", err)
	}
	if diagnostics.SkippedOperations != 1 || diagnostics.SkippedEvents != 2 {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	if len(diagnostics.Reasons) != 1 || !strings.Contains(diagnostics.Reasons[0], "reacquired without a pressure reservation") {
		t.Fatalf("reasons=%+v", diagnostics.Reasons)
	}
	if len(events) != len(healthy) {
		t.Fatalf("healthy operation lost: events=%+v", events)
	}
	for _, event := range events {
		if event.OperationID == badOperation {
			t.Fatalf("skipped operation leaked into the corpus: %+v", event)
		}
	}
}

func TestWorkEventStoreFailsClosedOnMalformedOrUnknownRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "work-events-20260714.jsonl")
	if err := os.WriteFile(path, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkEventStore(dir).Read(WorkEventFilter{}); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("malformed ledger unexpectedly accepted: %v", err)
	}
	event := validWorkEvent(time.Now(), WorkEventType("surprise"))
	event.EventID = workEventID(event.OperationID, event.Event, event.LeaseID)
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkEventStore(dir).Read(WorkEventFilter{}); err == nil || !strings.Contains(err.Error(), "unknown work event") {
		t.Fatalf("unknown ledger row unexpectedly accepted: %v", err)
	}
	if _, err := NewWorkEventStore(dir).Read(WorkEventFilter{Event: WorkEventType("surprise")}); err == nil {
		t.Fatal("unknown filter unexpectedly accepted")
	}
}

func TestWorkEventStoreFailsClosedOnFutureAndMultipleTerminalDrift(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	store := NewWorkEventStore(dir)
	store.Now = func() time.Time { return now }
	future := validWorkEvent(now.Add(2*maximumWorkClockSkew), WorkEventQueued)
	if err := store.AppendDurable(future); err == nil || !strings.Contains(err.Error(), "clock skew") {
		t.Fatalf("future event unexpectedly accepted: %v", err)
	}
	for _, eventType := range []WorkEventType{WorkEventQueued, WorkEventCompleted, WorkEventFailed} {
		event := validWorkEvent(now, eventType)
		if eventType == WorkEventFailed {
			event.LeaseID = workTestLease
		}
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}
	// Terminal drift is a per-operation lifecycle fault: the operation is dropped
	// and the reason reported, but it no longer blinds the rest of the corpus.
	events, diagnostics, err := store.ReadWithDiagnostics(WorkEventFilter{})
	if err != nil {
		t.Fatalf("terminal drift must degrade the operation, not the read: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("drifted operation leaked into the corpus: %+v", events)
	}
	if diagnostics.SkippedOperations != 1 || len(diagnostics.Reasons) != 1 ||
		!strings.Contains(diagnostics.Reasons[0], "multiple terminal") {
		t.Fatalf("multiple terminal drift was not reported: %+v", diagnostics)
	}
}

func TestWorkEventStoreToleratesHistoricalReconciliationExpiration(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	store := NewWorkEventStore(dir)
	store.Now = func() time.Time { return now.Add(2 * time.Second) }
	queued := validWorkEvent(now, WorkEventQueued)
	completed := validWorkEvent(now.Add(time.Second), WorkEventCompleted)
	completed.RuntimeMillis = 6000
	expired := validWorkEvent(now.Add(2*time.Second), WorkEventExpired)
	expired.LeaseID = workTestLease
	expired.Outcome = "dead_lease_owner"
	for _, event := range []WorkEvent{queued, completed, expired} {
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.Read(WorkEventFilter{})
	if err != nil || len(events) != 3 {
		t.Fatalf("historical reconciliation events=%+v err=%v", events, err)
	}
	replay := ReplayWorkEvents(events, testWorkLimits())
	if replay.Candidate.Operations != 1 {
		t.Fatalf("reconciliation expiration changed replay operations: %+v", replay.Candidate)
	}
}

func TestWorkEventStoreRecoversBoundedReconciliationTerminalRace(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 22, 22, 57, 45, 0, time.UTC)
	store := NewWorkEventStore(dir)
	store.Now = func() time.Time { return now.Add(2 * time.Second) }
	exit := 0
	queued := validWorkEvent(now, WorkEventQueued)
	expired := validWorkEvent(now.Add(time.Second), WorkEventExpired)
	expired.LeaseID = workTestLease
	expired.PID = 42696
	expired.Outcome = "dead_lease_owner"
	completed := validWorkEvent(now.Add(time.Second+77*time.Millisecond), WorkEventCompleted)
	completed.PID = expired.PID
	completed.ExitCode = &exit
	completed.RuntimeMillis = 183668
	for _, event := range []WorkEvent{queued, expired, completed} {
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.Read(WorkEventFilter{})
	if err != nil || len(events) != 3 {
		t.Fatalf("bounded reconciliation race events=%+v err=%v", events, err)
	}
	stats := SummarizeWorkEvents(events, time.Time{}, store.Now())
	if stats.EventCount != 3 || stats.OperationCount != 1 || len(stats.ByClass) != 1 ||
		stats.ByClass[0].Completed != 1 || stats.ByClass[0].Expired != 0 ||
		stats.ReviewSignals.ExpiredOwnerEvents != 1 || stats.ReviewSignals.ReconciliationRaceRecoveries != 1 ||
		len(stats.CalibrationCohorts) != 1 || stats.CalibrationCohorts[0].TerminalRuntimeSamples != 1 {
		t.Fatalf("bounded reconciliation race stats=%+v", stats)
	}
}

func TestWorkEventStoreRejectsUnboundedReconciliationTerminalRace(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 22, 22, 57, 45, 0, time.UTC)
	store := NewWorkEventStore(dir)
	store.Now = func() time.Time { return now.Add(10 * time.Second) }
	exit := 0
	queued := validWorkEvent(now, WorkEventQueued)
	expired := validWorkEvent(now.Add(time.Second), WorkEventExpired)
	expired.LeaseID = workTestLease
	expired.PID = 42696
	expired.Outcome = "dead_lease_owner"
	completed := validWorkEvent(now.Add(7*time.Second), WorkEventCompleted)
	completed.PID = expired.PID
	completed.ExitCode = &exit
	completed.RuntimeMillis = 1000
	for _, event := range []WorkEvent{queued, expired, completed} {
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}
	events, diagnostics, err := store.ReadWithDiagnostics(WorkEventFilter{})
	if err != nil {
		t.Fatalf("unbounded race must degrade the operation, not the read: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("unbounded race leaked into the corpus: %+v", events)
	}
	if diagnostics.SkippedOperations != 1 || len(diagnostics.Reasons) != 1 ||
		!strings.Contains(diagnostics.Reasons[0], "multiple terminal") {
		t.Fatalf("unbounded reconciliation race was not reported: %+v", diagnostics)
	}
}

func TestLateReconciliationTerminalRequiresExactDurableIdentity(t *testing.T) {
	now := time.Date(2026, 7, 22, 22, 57, 46, 0, time.UTC)
	exit := 0
	expired := validWorkEvent(now, WorkEventExpired)
	expired.LeaseID = workTestLease
	expired.PID = 42696
	expired.Outcome = "dead_lease_owner"
	completed := validWorkEvent(now.Add(77*time.Millisecond), WorkEventCompleted)
	completed.PID = expired.PID
	completed.ExitCode = &exit
	completed.RuntimeMillis = 1000
	if !isLateRealTerminalAfterReconciliation(expired, completed) {
		t.Fatal("exact durable reconciliation race was not recognized")
	}
	tests := map[string]func(*WorkEvent){
		"pid":            func(event *WorkEvent) { event.PID++ },
		"lease":          func(event *WorkEvent) { event.LeaseID = "00000000000000000000000000000003" },
		"missing exit":   func(event *WorkEvent) { event.ExitCode = nil },
		"missing digest": func(event *WorkEvent) { event.CommandDigest = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := completed
			mutate(&candidate)
			if isLateRealTerminalAfterReconciliation(expired, candidate) {
				t.Fatalf("mismatched %s unexpectedly recovered", name)
			}
		})
	}
}

func TestRecordExpiredSkipsOperationWithDurableTerminal(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	store := NewWorkEventStore(dir)
	store.Now = func() time.Time { return now }
	for _, eventType := range []WorkEventType{WorkEventQueued, WorkEventCompleted} {
		if err := store.AppendDurable(validWorkEvent(now, eventType)); err != nil {
			t.Fatal(err)
		}
	}
	coordinator := &WorkCoordinator{Dir: dir, EventStore: store}
	if err := coordinator.recordExpired(workTestOperation, workTestLease, WorkClassTest, 2, 123, "dead_lease_owner"); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read(WorkEventFilter{})
	if err != nil || len(events) != 2 {
		t.Fatalf("terminal operation gained cleanup terminal: events=%+v err=%v", events, err)
	}
}

func TestWorkEventPrivacyUsesOnlyOpaqueDigests(t *testing.T) {
	secretSession := "thread-with-sensitive-user-content"
	sessionDigest := DetectedAgentSessionDigest([]string{"CODEX_THREAD_ID=" + secretSession})
	if !validSHA256Digest(sessionDigest) || strings.Contains(sessionDigest, secretSession) {
		t.Fatalf("session identity was not privacy bounded: %q", sessionDigest)
	}
	digest := CommandShapeDigest("/Users/private/project/bin/tool", 4)
	if !validSHA256Digest(digest) || strings.Contains(digest, "private") {
		t.Fatalf("command shape was not opaque: %q", digest)
	}
	event := validWorkEvent(time.Now(), WorkEventQueued)
	event.SessionDigest = sessionDigest
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secretSession, "/Users/private", "TOKEN=", "--password"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("serialized work event leaked %q: %s", forbidden, body)
		}
	}
}

func TestSummarizeWorkEventsCountsPartialWindowsOnceAndOmitsEmptyClasses(t *testing.T) {
	now := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	exit := 0
	events := []WorkEvent{
		{OperationID: workTestOperation, Class: WorkClassBuild, Event: WorkEventAcquired, WaitMilliseconds: 10, PressureDimension: "cpu"},
		{OperationID: workTestOperation, Class: WorkClassBuild, Event: WorkEventCompleted, RuntimeMillis: 100, ExitCode: &exit, PressureDimension: "cpu"},
		// A late recovery row must remain visible as a signal without double
		// counting a second terminal result for the same operation.
		{OperationID: workTestOperation, Class: WorkClassBuild, Event: WorkEventExpired},
	}
	stats := SummarizeWorkEvents(events, now.Add(-time.Hour), now)
	if stats.OperationCount != 1 || stats.OpenOperations != 0 || len(stats.ByClass) != 1 || stats.ByClass[0].Class != WorkClassBuild || stats.ByClass[0].Operations != 1 || stats.ByClass[0].Completed != 1 || stats.ByClass[0].Expired != 0 || stats.ReviewSignals.ExpiredOwnerEvents != 1 || stats.ReviewSignals.CPUOnlyDeferrals != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	if stats.ServiceLevel.Status != "insufficient-data" || stats.ServiceLevel.Scope != WorkSlowdownSLOScope || stats.ServiceLevel.SampleScope != WorkSlowdownSLOSampleScope || stats.ServiceLevel.SamplesRequired != WorkP95MinimumSamples || stats.ServiceLevel.EvaluatedClasses != 0 || len(stats.ServiceLevel.EvaluatedSamples) != 0 || len(stats.ServiceLevel.DeferredClasses) != 1 || stats.ServiceLevel.DeferredClasses[0].Class != WorkClassBuild || stats.ServiceLevel.DeferredClasses[0].Samples != 1 || len(stats.ServiceLevel.Breaches) != 0 {
		t.Fatalf("service level=%+v", stats.ServiceLevel)
	}
}

func TestSummarizeWorkEventsTreatsStandaloneOwnerExpiryAsTerminal(t *testing.T) {
	now := time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC)
	events := []WorkEvent{
		{OperationID: workTestOperation, Class: WorkClassTest, Event: WorkEventQueued},
		{OperationID: workTestOperation, Class: WorkClassTest, Event: WorkEventExpired, WaitMilliseconds: 12_000, Outcome: "dead_waiter_owner"},
	}
	stats := SummarizeWorkEvents(events, now.Add(-time.Hour), now)
	if stats.OperationCount != 1 || stats.OpenOperations != 0 || stats.ReviewSignals.IncompleteOperations != 0 ||
		stats.ReviewSignals.ExpiredOwnerEvents != 1 || len(stats.ByClass) != 1 || stats.ByClass[0].Expired != 1 {
		t.Fatalf("standalone expiry was not terminal: %+v", stats)
	}
}

func TestSummarizeWorkEventsAuditsStructuralClassAdjustmentsOnce(t *testing.T) {
	now := time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC)
	secondOperation := "00000000000000000000000000000003"
	events := []WorkEvent{
		{OperationID: workTestOperation, RequestedClass: WorkClassBuild, Class: WorkClassExpressBuild, Event: WorkEventQueued},
		{OperationID: workTestOperation, Class: WorkClassExpressBuild, Event: WorkEventStarted},
		{OperationID: workTestOperation, Class: WorkClassExpressBuild, Event: WorkEventCompleted, RuntimeMillis: 100},
		{OperationID: secondOperation, RequestedClass: WorkClassExpressTest, Class: WorkClassTest, Event: WorkEventReused},
	}
	stats := SummarizeWorkEvents(events, now.Add(-time.Hour), now)
	if stats.ReviewSignals.ClassReclassifications != 2 || stats.ReviewSignals.FullToExpressAdjustments != 1 || stats.ReviewSignals.ExpressToFullAdjustments != 1 {
		t.Fatalf("class adjustment audit signals=%+v", stats.ReviewSignals)
	}
}

func TestSummarizeWorkEventsCountsCancelledCohortOnce(t *testing.T) {
	now := time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC)
	events := []WorkEvent{
		{SchemaVersion: WorkEventSchemaVersion, OperationID: workTestOperation, Class: WorkClassTest, Weight: 3, Event: WorkEventQueued},
		{SchemaVersion: WorkEventSchemaVersion, OperationID: workTestOperation, Class: WorkClassTest, Weight: 3, Event: WorkEventStarted, WaitMilliseconds: 10},
		{SchemaVersion: WorkEventSchemaVersion, OperationID: workTestOperation, Class: WorkClassTest, Weight: 3, Event: WorkEventCancelled, RuntimeMillis: 100, Outcome: "wrapper_interrupt", SchedulingPolicy: "test-policy", SelectorSchemaVersion: 7},
	}
	stats := SummarizeWorkEvents(events, now.Add(-time.Hour), now)
	if len(stats.ByClass) != 1 || stats.ByClass[0].Cancelled != 1 || len(stats.CalibrationCohorts) != 1 ||
		stats.CalibrationCohorts[0].TerminalRuntimeSamples != 1 || stats.PressureConditionedServiceLevel.TerminalRuntimeSamples != 1 {
		t.Fatalf("cancelled cohort was not counted exactly once: %+v", stats)
	}
}

func TestSummarizeWorkEventsReportsBoundedSlowdownSLOBreach(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	events := make([]WorkEvent, 0, WorkP95MinimumSamples*2)
	for index := range WorkP95MinimumSamples {
		operationID := fmt.Sprintf("%s-%02d", workTestOperation, index)
		events = append(events,
			WorkEvent{OperationID: operationID, Class: WorkClassBuild, Event: WorkEventAcquired, WaitMilliseconds: 60_000},
			WorkEvent{OperationID: operationID, Class: WorkClassBuild, Event: WorkEventCompleted, RuntimeMillis: 1_000},
		)
	}
	stats := SummarizeWorkEvents(events, now.Add(-time.Hour), now)
	if stats.ServiceLevel.Status != "breached" || stats.ServiceLevel.TargetP95BoundedSlowdown != WorkBoundedSlowdownP95Target || stats.ServiceLevel.EvaluatedClasses != 1 || len(stats.ServiceLevel.EvaluatedSamples) != 1 || stats.ServiceLevel.EvaluatedSamples[0].Class != WorkClassBuild || stats.ServiceLevel.EvaluatedSamples[0].Samples != WorkP95MinimumSamples || len(stats.ServiceLevel.DeferredClasses) != 0 || len(stats.ServiceLevel.Breaches) != 1 || stats.ServiceLevel.Breaches[0].Class != WorkClassBuild {
		t.Fatalf("service level=%+v", stats.ServiceLevel)
	}
}

func TestSummarizeWorkEventsAddsPressureConditionedEvidenceWithoutMaskingEndToEndBreach(t *testing.T) {
	now := time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC)
	events := make([]WorkEvent, 0, WorkP95MinimumSamples*3)
	for index := range WorkP95MinimumSamples {
		operationID := fmt.Sprintf("%s-pressure-%02d", workTestOperation, index)
		events = append(events,
			WorkEvent{SchemaVersion: WorkEventSchemaVersion, OperationID: operationID, Class: WorkClassBuild, Event: WorkEventQueued, PressureDimension: "cpu"},
			WorkEvent{SchemaVersion: WorkEventSchemaVersion, OperationID: operationID, Class: WorkClassBuild, Event: WorkEventAcquired, WaitMilliseconds: 60_000},
			WorkEvent{
				SchemaVersion: WorkEventSchemaVersion, OperationID: operationID, Class: WorkClassBuild,
				Event: WorkEventCompleted, WaitMilliseconds: 60_000, AdmissionWaitMilliseconds: 55_000,
				RuntimeMillis: 1_000,
			},
		)
	}

	stats := SummarizeWorkEvents(events, now.Add(-time.Hour), now)
	if stats.ServiceLevel.Status != "breached" || len(stats.ServiceLevel.Breaches) != 1 {
		t.Fatalf("end-to-end authority was masked: %+v", stats.ServiceLevel)
	}
	conditioned := stats.PressureConditionedServiceLevel
	if conditioned.SchemaVersion != 1 || conditioned.EvidenceStatus != "complete" || conditioned.Scope != WorkPressureConditionedScope ||
		conditioned.Method != WorkPressureConditionedMethod || !conditioned.InformationalOnly ||
		!conditioned.EndToEndAuthoritative || conditioned.Status != "met" ||
		conditioned.TerminalRuntimeSamples != WorkP95MinimumSamples || conditioned.EligibleSamples != WorkP95MinimumSamples ||
		conditioned.PressureAffectedSamples != WorkP95MinimumSamples || conditioned.ExcludedWaitMS != 1_100_000 ||
		conditioned.WindowBoundarySamples != 0 || conditioned.LegacySchemaSamples != 0 ||
		conditioned.InvalidDecompositionSamples != 0 || len(conditioned.Breaches) != 0 || len(conditioned.ByClass) != 1 {
		t.Fatalf("pressure-conditioned evidence=%+v", conditioned)
	}
	row := conditioned.ByClass[0]
	if row.Class != WorkClassBuild || row.Samples != WorkP95MinimumSamples || row.PressureAffectedSamples != WorkP95MinimumSamples ||
		row.ExcludedWaitMS != 1_100_000 || row.P95BoundedSlowdown != 0.6 || row.Status != "met" {
		t.Fatalf("pressure-conditioned class=%+v", row)
	}
}

func TestPressureConditionedEvidenceFailsClosedOnPartialOrInvalidDecomposition(t *testing.T) {
	now := time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC)
	events := []WorkEvent{
		// The queued event is outside the requested window, so this lifecycle
		// cannot prove whether admission wait was pressure-driven.
		{SchemaVersion: WorkEventSchemaVersion, OperationID: workTestOperation + "-boundary", Class: WorkClassTest, Event: WorkEventCompleted, WaitMilliseconds: 20_000, AdmissionWaitMilliseconds: 15_000, RuntimeMillis: 1_000},
		{SchemaVersion: WorkEventSchemaVersion, OperationID: workTestOperation + "-invalid", Class: WorkClassTest, Event: WorkEventQueued, PressureDimension: "cpu"},
		{SchemaVersion: WorkEventSchemaVersion, OperationID: workTestOperation + "-invalid", Class: WorkClassTest, Event: WorkEventCompleted, WaitMilliseconds: 10_000, AdmissionWaitMilliseconds: 11_000, RuntimeMillis: 1_000},
		{SchemaVersion: WorkEventSchemaVersion - 1, OperationID: workTestOperation + "-legacy", Class: WorkClassTest, Event: WorkEventQueued, PressureDimension: "cpu"},
		{SchemaVersion: WorkEventSchemaVersion - 1, OperationID: workTestOperation + "-legacy", Class: WorkClassTest, Event: WorkEventCompleted, WaitMilliseconds: 10_000, AdmissionWaitMilliseconds: 9_000, RuntimeMillis: 1_000},
	}

	conditioned := SummarizeWorkEvents(events, now.Add(-time.Hour), now).PressureConditionedServiceLevel
	if conditioned.Status != "insufficient-data" || conditioned.EvidenceStatus != "invalid" || conditioned.TerminalRuntimeSamples != 3 || conditioned.EligibleSamples != 0 ||
		conditioned.WindowBoundarySamples != 1 || conditioned.InvalidDecompositionSamples != 1 || conditioned.LegacySchemaSamples != 1 ||
		len(conditioned.ByClass) != 0 || len(conditioned.EvaluatedSamples) != 0 || len(conditioned.DeferredClasses) != 0 {
		t.Fatalf("partial evidence did not fail closed: %+v", conditioned)
	}
}

func TestSummarizeWorkEventsCountsFailedTerminalRuntimeSamples(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	events := make([]WorkEvent, 0, WorkP95MinimumSamples*2)
	for index := range WorkP95MinimumSamples {
		operationID := fmt.Sprintf("%s-failed-%02d", workTestOperation, index)
		events = append(events, WorkEvent{OperationID: operationID, Class: WorkClassBenchmark, Event: WorkEventAcquired, WaitMilliseconds: 20_000})
		terminal := WorkEventCompleted
		if index%2 == 1 {
			terminal = WorkEventFailed
		}
		events = append(events, WorkEvent{OperationID: operationID, Class: WorkClassBenchmark, Event: terminal, RuntimeMillis: 1_000})
	}
	stats := SummarizeWorkEvents(events, now.Add(-time.Hour), now)
	if len(stats.ByClass) != 1 || stats.ByClass[0].Completed != 10 || stats.ByClass[0].Failed != 10 || stats.ServiceLevel.SampleScope != WorkSlowdownSLOSampleScope || len(stats.ServiceLevel.EvaluatedSamples) != 1 || stats.ServiceLevel.EvaluatedSamples[0].Samples != WorkP95MinimumSamples {
		t.Fatalf("terminal-runtime sample contract: %+v", stats)
	}
}

func TestSummarizeWorkEventsDefersUnderSampledSLOBreach(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	events := []WorkEvent{
		{OperationID: workTestOperation, Class: WorkClassBuild, Event: WorkEventAcquired, WaitMilliseconds: 60_000},
		{OperationID: workTestOperation, Class: WorkClassBuild, Event: WorkEventCompleted, RuntimeMillis: 1_000},
	}
	stats := SummarizeWorkEvents(events, now.Add(-time.Hour), now)
	if stats.ServiceLevel.Status != "insufficient-data" || stats.ServiceLevel.EvaluatedClasses != 0 || len(stats.ServiceLevel.DeferredClasses) != 1 || stats.ServiceLevel.DeferredClasses[0].Class != WorkClassBuild || stats.ServiceLevel.DeferredClasses[0].Samples != 1 || len(stats.ServiceLevel.Breaches) != 0 {
		t.Fatalf("service level=%+v", stats.ServiceLevel)
	}
}

func TestSummarizeWorkEventsCountsStorageDeferralOncePerOperation(t *testing.T) {
	now := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	events := []WorkEvent{
		{OperationID: workTestOperation, Class: WorkClassBuild, Event: WorkEventQueued, PressureDimension: "storage"},
		{OperationID: workTestOperation, Class: WorkClassBuild, Event: WorkEventQueued, PressureDimension: "storage"},
	}
	stats := SummarizeWorkEvents(events, now.Add(-time.Hour), now)
	if stats.ReviewSignals.StorageDeferrals != 1 || stats.ReviewSignals.MemoryDeferrals != 0 || stats.ReviewSignals.CPUOnlyDeferrals != 0 {
		t.Fatalf("review signals=%+v", stats.ReviewSignals)
	}
}

func TestWorkEvaluationIsDeterministicWithinResourceBudgets(t *testing.T) {
	policy := DefaultPolicy(16 * 1024)
	first := EvaluateWorkSystem(policy)
	second := EvaluateWorkSystem(policy)
	sharedProjection := ProjectTelemetryBytesPerDay(policy, 0, 0, 0)
	if first.ProjectedMonitorBytesDay != sharedProjection.MonitorBytes || first.ProjectedTelemetryBytesDay != sharedProjection.TotalBytes {
		t.Fatalf("evaluator monitor=%d total=%d shared=%+v", first.ProjectedMonitorBytesDay, first.ProjectedTelemetryBytesDay, sharedProjection)
	}
	if !first.OK || !second.OK {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if first.DeterministicDigest != second.DeterministicDigest || first.ScenarioCount != first.Passed+first.Failed {
		t.Fatalf("evaluation was not stable: first=%+v second=%+v", first, second)
	}
	if first.ProjectedMonitorBytesDay > first.Thresholds.TargetMonitorBytesDay || first.MaximumWorkEventBytes > first.Thresholds.MaximumWorkEventBytes {
		t.Fatalf("evaluation exceeded budget: %+v", first)
	}
	if first.ProjectedTelemetryBytesDay > first.Thresholds.MaximumMonitorBytesDay {
		t.Fatalf("total telemetry exceeded budget: %+v", first)
	}
}

func TestReplayWorkEventsComparesFIFOAndBoundedLookaheadDeterministically(t *testing.T) {
	start := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	exit := 0
	events := []WorkEvent{}
	add := func(id string, class WorkClass, queued time.Time, runtime time.Duration) {
		events = append(events,
			WorkEvent{OperationID: id, Class: class, Event: WorkEventQueued, Timestamp: queued},
			WorkEvent{OperationID: id, Class: class, Event: WorkEventCompleted, Timestamp: queued.Add(runtime), RuntimeMillis: runtime.Milliseconds(), ExitCode: &exit},
		)
	}
	add("00000000000000000000000000000001", WorkClassEmulator, start, 40*time.Second)
	add("00000000000000000000000000000002", WorkClassBuild, start.Add(time.Second), 40*time.Second)
	add("00000000000000000000000000000003", WorkClassBrowser, start.Add(2*time.Second), time.Second)
	first := ReplayWorkEvents(events, testWorkLimits())
	second := ReplayWorkEvents(events, testWorkLimits())
	benchmark := WorkSelectorBenchmark{Pass: true, P95Microseconds: 1, BudgetMicroseconds: 2000}
	first.ApplySelectorBenchmark(benchmark)
	second.ApplySelectorBenchmark(benchmark)
	if first.Candidate.CapacityViolations != 0 || first.Candidate.ProtectedBypassViolations != 0 || first.Candidate.MaximumBypasses > workMaximumBypasses {
		t.Fatalf("candidate replay unsafe: %+v", first)
	}
	if first.Candidate.ByClass[2].WaitP95MS >= first.FIFO.ByClass[2].WaitP95MS {
		t.Fatalf("browser did not benefit from feasible lookahead: fifo=%+v candidate=%+v", first.FIFO, first.Candidate)
	}
	if first.RegressionSamplesRequired != WorkReplayMinimumRegressionSamples || first.RegressionSampleScope != WorkSlowdownSLOSampleScope || len(first.RegressionClassesEvaluated) != 0 || len(first.RegressionEvaluatedSamples) != 0 || len(first.RegressionClassesDeferred) != 3 {
		t.Fatalf("small per-class populations must be explicit deferred evidence: %+v", first)
	}
	first.TraceStart, first.TraceEnd = time.Time{}, time.Time{}
	second.TraceStart, second.TraceEnd = time.Time{}, time.Time{}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replay was nondeterministic: first=%+v second=%+v", first, second)
	}
}

func TestReplayWorkEventsProvesBuildCalibrationPackingGain(t *testing.T) {
	start := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	events := []WorkEvent{}
	add := func(id string, class WorkClass, queued time.Time, runtime time.Duration) {
		events = append(events,
			WorkEvent{OperationID: id, Class: class, Event: WorkEventQueued, Timestamp: queued},
			WorkEvent{OperationID: id, Class: class, Event: WorkEventCompleted, Timestamp: queued.Add(runtime), RuntimeMillis: runtime.Milliseconds()},
		)
	}
	add("00000000000000000000000000000001", WorkClassTest, start, 40*time.Second)
	add("00000000000000000000000000000002", WorkClassBuild, start.Add(time.Second), time.Second)
	replay := ReplayWorkEvents(events, defaultWorkLimits(10))
	candidate := replayClassMap(replay.Candidate.ByClass)[WorkClassBuild]
	legacy := replayClassMap(replay.LegacyCalibration.ByClass)[WorkClassBuild]
	if candidate.WaitP95MS != 0 || legacy.WaitP95MS != 39_000 || !replay.CalibrationGatesPass {
		t.Fatalf("build calibration replay: candidate=%+v legacy=%+v gates=%v", candidate, legacy, replay.CalibrationGatesPass)
	}
}
