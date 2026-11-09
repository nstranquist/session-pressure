package sessionpressure

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func indexTestOperation(i int) string {
	return fmt.Sprintf("%032x", 0xabc000+i)
}

// appendIndexFixtureOperation writes one complete operation lifecycle.
func appendIndexFixtureOperation(t *testing.T, store *WorkEventStore, at time.Time, operationID string, class WorkClass) {
	t.Helper()
	for _, eventType := range []WorkEventType{WorkEventQueued, WorkEventAcquired, WorkEventStarted, WorkEventCompleted} {
		event := validWorkEvent(at, eventType)
		event.OperationID = operationID
		event.Class = class
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}
}

func indexFixtureStore(t *testing.T) (*WorkEventStore, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	store := NewWorkEventStore(t.TempDir())
	store.Now = func() time.Time { return now }
	return store, now
}

// The index answers every filter exactly like the index-free full scan.
func TestWorkEventIndexMatchesFullScan(t *testing.T) {
	store, now := indexFixtureStore(t)
	for i := 0; i < 6; i++ {
		class := WorkClassTest
		if i%2 == 0 {
			class = WorkClassBuild
		}
		appendIndexFixtureOperation(t, store, now.Add(-time.Duration(i)*20*time.Hour), indexTestOperation(i), class)
	}
	maximum := now.Add(maximumWorkClockSkew)
	filters := []WorkEventFilter{
		{},
		{Since: now.Add(-25 * time.Hour)},
		{Class: WorkClassBuild},
		{Event: WorkEventCompleted},
		{OperationID: indexTestOperation(3)},
		{Since: now.Add(-50 * time.Hour), Class: WorkClassTest, Limit: 3},
	}
	for _, filter := range filters {
		got, gotDiagnostics, err := store.ReadWithDiagnostics(filter)
		if err != nil {
			t.Fatalf("indexed read %+v: %v", filter, err)
		}
		scanned, err := store.scanWorkEventRows(maximum)
		if err != nil {
			t.Fatal(err)
		}
		want, wantDiagnostics, err := finishWorkEventRead(scanned, filter)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(eventIDs(got), eventIDs(want)) {
			t.Fatalf("filter %+v: indexed %v, scan %v", filter, eventIDs(got), eventIDs(want))
		}
		if !reflect.DeepEqual(gotDiagnostics, wantDiagnostics) {
			t.Fatalf("filter %+v: diagnostics indexed %+v, scan %+v", filter, gotDiagnostics, wantDiagnostics)
		}
	}
}

func eventIDs(events []WorkEvent) []string {
	out := make([]string, len(events))
	for i, event := range events {
		out[i] = event.EventID
	}
	return out
}

// Older binaries append JSONL without touching the index; the next read
// imports their lines.
func TestWorkEventIndexImportsLinesFromIndexUnawareWriters(t *testing.T) {
	store, now := indexFixtureStore(t)
	appendIndexFixtureOperation(t, store, now, indexTestOperation(1), WorkClassTest)
	if events, err := store.Read(WorkEventFilter{}); err != nil || len(events) != 4 {
		t.Fatalf("initial read=%d err=%v", len(events), err)
	}
	// Simulate an old writer: raw JSONL append, no index import.
	path := store.dayPath(now)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []WorkEventType{WorkEventQueued, WorkEventAcquired, WorkEventStarted, WorkEventCompleted} {
		event := validWorkEvent(now, eventType)
		event.OperationID = indexTestOperation(2)
		event.EventID = workEventIdentity(event)
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(body, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read(WorkEventFilter{OperationID: indexTestOperation(2)})
	if err != nil || len(events) != 4 {
		t.Fatalf("read after raw append=%d err=%v", len(events), err)
	}
	found, err := store.operationHasTerminal(indexTestOperation(2))
	if err != nil || !found {
		t.Fatalf("operationHasTerminal=%v err=%v", found, err)
	}
}

// AppendDurable stays idempotent through the index.
func TestWorkEventIndexKeepsAppendIdempotent(t *testing.T) {
	store, now := indexFixtureStore(t)
	event := validWorkEvent(now, WorkEventQueued)
	for i := 0; i < 3; i++ {
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(store.dayPath(now))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(body), "\n"); lines != 1 {
		t.Fatalf("ledger lines=%d, want 1", lines)
	}
}

// A duplicate event_id written into the ledger still fails closed.
func TestWorkEventIndexFailsClosedOnLedgerDuplicate(t *testing.T) {
	store, now := indexFixtureStore(t)
	appendIndexFixtureOperation(t, store, now, indexTestOperation(1), WorkClassTest)
	path := store.dayPath(now)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(string(body), "\n", 2)[0]
	if err := os.WriteFile(path, append(body, []byte(first+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(WorkEventFilter{}); err == nil || !strings.Contains(err.Error(), "duplicate work event_id") {
		t.Fatalf("read err=%v, want duplicate work event_id", err)
	}
}

// A replaced shard (different first line) is re-imported, and a pruned shard
// leaves the index.
func TestWorkEventIndexReimportsReplacedShardAndForgetsPruned(t *testing.T) {
	store, now := indexFixtureStore(t)
	appendIndexFixtureOperation(t, store, now, indexTestOperation(1), WorkClassTest)
	if events, err := store.Read(WorkEventFilter{}); err != nil || len(events) != 4 {
		t.Fatalf("initial read=%d err=%v", len(events), err)
	}
	if err := os.Remove(store.dayPath(now)); err != nil {
		t.Fatal(err)
	}
	appendIndexFixtureOperation(t, store, now, indexTestOperation(2), WorkClassBuild)
	events, err := store.Read(WorkEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].OperationID != indexTestOperation(2) {
		t.Fatalf("replaced shard read=%v", eventIDs(events))
	}

	old := now.AddDate(0, 0, -30)
	oldStore := NewWorkEventStore(store.Dir)
	oldStore.Now = func() time.Time { return old }
	appendIndexFixtureOperation(t, oldStore, old, indexTestOperation(3), WorkClassTest)
	if events, err := store.Read(WorkEventFilter{}); err != nil || len(events) != 8 {
		t.Fatalf("two-shard read=%d err=%v", len(events), err)
	}
	if err := store.Prune(14); err != nil {
		t.Fatal(err)
	}
	db, err := store.workEventIndex()
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_events`).Scan(&rows); err != nil || rows != 4 {
		t.Fatalf("index rows after prune=%d err=%v, want 4", rows, err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir, workEventIndexName)); err != nil {
		t.Fatal(err)
	}
}

// Diagnostics describe the operations a read covers: a malformed operation
// that ended before the window is reported by a whole-ledger read, not by a
// 24h report (the index-free scan reported it on every read).
func TestWorkEventIndexScopesDiagnosticsToReadWindow(t *testing.T) {
	store, now := indexFixtureStore(t)
	appendIndexFixtureOperation(t, store, now.Add(-time.Hour), indexTestOperation(1), WorkClassTest)
	old := now.Add(-72 * time.Hour)
	bad := indexTestOperation(9)
	for index, eventType := range []WorkEventType{WorkEventQueued, WorkEventReacquired} {
		event := validWorkEvent(old.Add(time.Duration(index)*time.Minute), eventType)
		event.OperationID = bad
		if eventType == WorkEventReacquired {
			event.LeaseID = workTestLease
		}
		if err := store.AppendDurable(event); err != nil {
			t.Fatal(err)
		}
	}
	events, diagnostics, err := store.ReadWithDiagnostics(WorkEventFilter{Since: now.Add(-24 * time.Hour)})
	if err != nil || len(events) != 4 || diagnostics.SkippedOperations != 0 {
		t.Fatalf("window read events=%d diagnostics=%+v err=%v", len(events), diagnostics, err)
	}
	events, diagnostics, err = store.ReadWithDiagnostics(WorkEventFilter{})
	if err != nil || len(events) != 4 || diagnostics.SkippedOperations != 1 {
		t.Fatalf("ledger read events=%d diagnostics=%+v err=%v", len(events), diagnostics, err)
	}
}
