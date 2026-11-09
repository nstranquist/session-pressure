package sessionpressure

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Work-event read index.
//
// The day-sharded JSONL ledger stays the durable write log: every installed
// binary (session-pressure, ndev, the menu bar app) appends to it, and older
// binaries know nothing else. Lookups used to parse every retained shard
// (about 15 MB over 14 days): AppendDurable re-read the whole day file to
// deduplicate before each append, operationHasTerminal scanned every file per
// lease expiry, and readAll parsed everything even for a 1h window.
//
// work-events.sqlite is a rebuildable read model over that log. Before each
// query the index imports only the bytes appended since its stored offset,
// in the same transaction that advances the offset, so concurrent readers
// never import a line twice and a crash never skips one. A shard that shrank
// or was replaced is re-imported. The resident monitor's day-bounded
// CountWrapperInterruptOperations keeps streaming JSONL so the resident never
// loads a database engine.

const (
	workEventIndexName         = "work-events.sqlite"
	workEventIndexSchema       = 1
	workEventIndexBusyTimeout  = 5 * time.Second
	workEventIndexMaxLineBytes = 1 << 20
	// A sync that imports this many lines (a first build or a long gap)
	// truncates the WAL, so later short-lived readers do not walk it.
	workEventIndexCheckpointLines = 2048
)

const workEventIndexDDL = `
CREATE TABLE IF NOT EXISTS work_event_index_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS work_event_shards (
  name        TEXT PRIMARY KEY,
  offset      INTEGER NOT NULL,
  lines       INTEGER NOT NULL,
  first_line  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS work_events (
  event_id     TEXT PRIMARY KEY,
  shard        TEXT NOT NULL,
  line         INTEGER NOT NULL,
  ts_unix_nano INTEGER NOT NULL,
  event        TEXT NOT NULL,
  operation_id TEXT NOT NULL,
  class        TEXT NOT NULL,
  row_json     BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_work_events_ts ON work_events(ts_unix_nano);
CREATE INDEX IF NOT EXISTS idx_work_events_operation ON work_events(operation_id, event);
CREATE INDEX IF NOT EXISTS idx_work_events_operation_ts ON work_events(operation_id, ts_unix_nano);
CREATE INDEX IF NOT EXISTS idx_work_events_shard ON work_events(shard, line);
`

var errWorkEventIndexDisabled = errors.New("work event index disabled")

var (
	workEventIndexMu    sync.Mutex
	workEventIndexCache = map[string]*sql.DB{}
)

// workEventIndex opens (once per process and directory) the read index.
func (store *WorkEventStore) workEventIndex() (*sql.DB, error) {
	if store.scanOnly {
		return nil, errWorkEventIndexDisabled
	}
	dir, err := filepath.Abs(store.Dir)
	if err != nil {
		return nil, err
	}
	workEventIndexMu.Lock()
	defer workEventIndexMu.Unlock()
	if db, ok := workEventIndexCache[dir]; ok {
		return db, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, workEventIndexName)
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", workEventIndexBusyTimeout.Milliseconds()))
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(NORMAL)")
	query.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+query.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(workEventIndexDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("work event index schema: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		db.Close()
		return nil, err
	}
	var version string
	err = db.QueryRow(`SELECT value FROM work_event_index_meta WHERE key = 'schema'`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.Exec(`INSERT OR IGNORE INTO work_event_index_meta(key, value) VALUES ('schema', ?)`, fmt.Sprint(workEventIndexSchema))
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("work event index meta: %w", err)
	}
	workEventIndexCache[dir] = db
	return db, nil
}

// syncWorkEventIndex imports unindexed JSONL lines from every shard (or only
// the named shard) and drops rows of shards that no longer exist.
func (store *WorkEventStore) syncWorkEventIndex(db *sql.DB, onlyShard string) error {
	paths, err := filepath.Glob(filepath.Join(store.Dir, "work-events-*.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	present := make(map[string]bool, len(paths))
	var imported int64
	for _, path := range paths {
		name := filepath.Base(path)
		present[name] = true
		if onlyShard != "" && name != onlyShard {
			continue
		}
		count, err := syncWorkEventShard(db, path)
		if err != nil {
			return err
		}
		imported += count
	}
	if imported >= workEventIndexCheckpointLines {
		// Best effort: a busy reader only delays the truncation.
		_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	if onlyShard != "" {
		return nil
	}
	rows, err := db.Query(`SELECT name FROM work_event_shards`)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if !present[name] {
			gone = append(gone, name)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, name := range gone {
		if err := dropWorkEventShard(db, name); err != nil {
			return err
		}
	}
	return nil
}

func dropWorkEventShard(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM work_events WHERE shard = ?`, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM work_event_shards WHERE name = ?`, name); err != nil {
		return err
	}
	return tx.Commit()
}

// syncWorkEventShard imports the complete lines appended to one shard since
// its stored offset. The offset is read and advanced inside one immediate
// transaction, so concurrent syncs serialize instead of double-importing.
func syncWorkEventShard(db *sql.DB, path string) (imported int64, resultErr error) {
	name := filepath.Base(path)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if resultErr != nil {
			_ = tx.Rollback()
		}
	}()
	var offset, lines int64
	var firstLine string
	err = tx.QueryRow(`SELECT offset, lines, first_line FROM work_event_shards WHERE name = ?`, name).Scan(&offset, &lines, &firstLine)
	known := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if known && info.Size() == offset {
		return 0, tx.Rollback()
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	currentFirst, err := firstWorkEventLine(file)
	if err != nil {
		return 0, err
	}
	if known && (info.Size() < offset || currentFirst != firstLine) {
		// Shrunk or replaced: the indexed rows no longer describe this file.
		if _, err := tx.Exec(`DELETE FROM work_events WHERE shard = ?`, name); err != nil {
			return 0, err
		}
		offset, lines, known = 0, 0, false
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	insert, err := tx.Prepare(`INSERT INTO work_events(event_id, shard, line, ts_unix_nano, event, operation_id, class, row_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insert.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		raw, readErr := reader.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			// An in-flight append; import it on the next sync.
			break
		}
		if len(raw) > 0 {
			if len(raw) > workEventIndexMaxLineBytes {
				return 0, fmt.Errorf("work event %s line %d exceeds %d bytes", name, lines+1, workEventIndexMaxLineBytes)
			}
			lines++
			imported++
			offset += int64(len(raw))
			body := bytes.TrimSpace(raw)
			var event WorkEvent
			if err := json.Unmarshal(body, &event); err != nil {
				return 0, fmt.Errorf("decode work event %s line %d: %w", name, lines, err)
			}
			if err := event.Validate(); err != nil {
				return 0, fmt.Errorf("validate work event %s line %d: %w", name, lines, err)
			}
			if _, err := insert.Exec(event.EventID, name, lines, event.Timestamp.UTC().UnixNano(),
				string(event.Event), event.OperationID, string(event.Class), body); err != nil {
				if isSQLiteConstraint(err) {
					return 0, fmt.Errorf("duplicate work event_id %s", event.EventID)
				}
				return 0, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return 0, readErr
		}
	}
	if _, err := tx.Exec(`INSERT INTO work_event_shards(name, offset, lines, first_line) VALUES (?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET offset = excluded.offset, lines = excluded.lines, first_line = excluded.first_line`,
		name, offset, lines, currentFirst); err != nil {
		return 0, err
	}
	return imported, tx.Commit()
}

// firstWorkEventLine returns the shard's first complete line (bounded), which
// identifies its generation.
func firstWorkEventLine(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, workEventIndexMaxLineBytes), 64*1024)
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func isSQLiteConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed")
}

// indexedWorkEventExists reports whether eventID is already in the ledger.
// The caller holds the day shard's identity lock.
func (store *WorkEventStore) indexedWorkEventExists(path, eventID string) (bool, error) {
	db, err := store.workEventIndex()
	if err != nil {
		return false, err
	}
	if err := store.syncWorkEventIndex(db, filepath.Base(path)); err != nil {
		return false, err
	}
	var one int
	err = db.QueryRow(`SELECT 1 FROM work_events WHERE event_id = ?`, eventID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// indexAppendedWorkEvent imports the line just appended so the next lookup
// does not have to.
func (store *WorkEventStore) indexAppendedWorkEvent(path string) error {
	db, err := store.workEventIndex()
	if err != nil {
		return err
	}
	return store.syncWorkEventIndex(db, filepath.Base(path))
}

func (store *WorkEventStore) indexedOperationHasTerminal(operationID string) (bool, error) {
	db, err := store.workEventIndex()
	if err != nil {
		return false, err
	}
	if err := store.syncWorkEventIndex(db, ""); err != nil {
		return false, err
	}
	var one int
	err = db.QueryRow(`SELECT 1 FROM work_events WHERE operation_id = ? AND event IN (?, ?, ?, ?, ?) LIMIT 1`,
		operationID, string(WorkEventCompleted), string(WorkEventFailed), string(WorkEventCancelled),
		string(WorkEventExpired), string(WorkEventReused)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// indexedWorkEventRows returns every row of every operation that has at least
// one event matching filter (lifecycle validation needs whole operations). It
// fails closed on a timestamp beyond maximumTimestamp anywhere in the ledger,
// as the full scan did. Row order is irrelevant: finishWorkEventRead sorts by
// a total order.
func (store *WorkEventStore) indexedWorkEventRows(filter WorkEventFilter, maximumTimestamp time.Time) ([]WorkEvent, error) {
	db, err := store.workEventIndex()
	if err != nil {
		return nil, err
	}
	if err := store.syncWorkEventIndex(db, ""); err != nil {
		return nil, err
	}
	var futureID string
	var futureTS int64
	err = db.QueryRow(`SELECT event_id, ts_unix_nano FROM work_events WHERE ts_unix_nano > ? LIMIT 1`, maximumTimestamp.UnixNano()).Scan(&futureID, &futureTS)
	if err == nil {
		return nil, fmt.Errorf("work event %s timestamp %s is beyond allowed clock skew", futureID, time.Unix(0, futureTS).UTC().Format(time.RFC3339Nano))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if filter.OperationID != "" {
		return queryWorkEventRows(db, nil, `SELECT operation_id, row_json FROM work_events WHERE operation_id = ?`, filter.OperationID)
	}
	var where []string
	var args []any
	if !filter.Since.IsZero() {
		where = append(where, "ts_unix_nano >= ?")
		args = append(args, filter.Since.UTC().UnixNano())
	}
	if filter.Class != "" {
		where = append(where, "class = ?")
		args = append(args, string(filter.Class))
	}
	if filter.Event != "" {
		where = append(where, "event = ?")
		args = append(args, string(filter.Event))
	}
	if len(where) == 0 {
		return queryWorkEventRows(db, nil, `SELECT operation_id, row_json FROM work_events`)
	}
	// Operations with a matching event, and the earliest event of any of
	// them: every row they own lies at or after that bound. Both lookups use
	// indexes; a correlated IN subquery cost ~10x more.
	matching := map[string]struct{}{}
	opRows, err := db.Query(`SELECT DISTINCT operation_id FROM work_events WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	for opRows.Next() {
		var operationID string
		if err := opRows.Scan(&operationID); err != nil {
			opRows.Close()
			return nil, err
		}
		matching[operationID] = struct{}{}
	}
	if err := errors.Join(opRows.Err(), opRows.Close()); err != nil {
		return nil, err
	}
	if len(matching) == 0 {
		return []WorkEvent{}, nil
	}
	low := int64(0)
	found := false
	spans, err := db.Query(`SELECT operation_id, MIN(ts_unix_nano) FROM work_events GROUP BY operation_id`)
	if err != nil {
		return nil, err
	}
	for spans.Next() {
		var operationID string
		var first int64
		if err := spans.Scan(&operationID, &first); err != nil {
			spans.Close()
			return nil, err
		}
		if _, ok := matching[operationID]; ok && (!found || first < low) {
			low, found = first, true
		}
	}
	if err := errors.Join(spans.Err(), spans.Close()); err != nil {
		return nil, err
	}
	return queryWorkEventRows(db, matching, `SELECT operation_id, row_json FROM work_events WHERE ts_unix_nano >= ?`, low)
}

// queryWorkEventRows decodes (operation_id, row_json) rows, keeping only
// operations in keep when keep is non-nil.
func queryWorkEventRows(db *sql.DB, keep map[string]struct{}, query string, args ...any) ([]WorkEvent, error) {
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkEvent, 0)
	for rows.Next() {
		var operationID string
		var body []byte
		if err := rows.Scan(&operationID, &body); err != nil {
			return nil, err
		}
		if keep != nil {
			if _, ok := keep[operationID]; !ok {
				continue
			}
		}
		var event WorkEvent
		if err := json.Unmarshal(body, &event); err != nil {
			return nil, fmt.Errorf("decode indexed work event: %w", err)
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

// pruneWorkEventIndex drops index rows of shards Prune removed.
func (store *WorkEventStore) pruneWorkEventIndex() error {
	db, err := store.workEventIndex()
	if err != nil {
		return err
	}
	return store.syncWorkEventIndex(db, "")
}
