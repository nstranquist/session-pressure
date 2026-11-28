package gocachelru

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

// The cache index lives in SQLite (WORK-1601). It replaced an append-only
// JSONL journal that every client replayed before its first put: on
// runner-3 (2026-11-21) the journal held about 1.1 M rows (135 MB), and the
// replay cost each Go process that put anything about 3.8 s and 622 MB under
// the build lane's CPU quota, plus two fsyncs per put and a second replay at
// close. A provider build whose compiles all hit the cache still paid it for
// its one link put. With SQLite a put is one indexed UPSERT transaction, the
// eviction pass reads at most a few hundred rows in access order, and the
// totals come from one row that triggers keep current.
//
// Concurrency is unchanged: the authority flock still serializes puts,
// eviction and the startup sweep of dead clients, so a pin and an eviction
// never race. SQLite's own locking only orders those writers against the
// get-only clients that flush access hints at close without the flock.
//
// Mixed versions: a binary from before this change still appends to
// index.jsonl. The first new client imports that journal once, and every
// later put imports the rows appended since (the import offset and the
// journal's first-row generation are kept in the meta table), so objects that
// an old client puts or evicts stay accounted for.

const (
	indexDBFile       = "index.sqlite"
	legacyJournalFile = "index.jsonl"
	evictLockFile     = "evict.lock"
	indexSchemaVer    = 1
	// evictionBudget bounds the candidates one pass examines.
	evictionBudget = 256
)

// hintBusyTimeout bounds how long a get-only client waits to record its
// access hints at close. Hints only refine the eviction order, so a client
// that cannot write them in time drops them.
var hintBusyTimeout = 2 * time.Second

const indexSchema = `
CREATE TABLE IF NOT EXISTS entries(
	key TEXT PRIMARY KEY,
	kind INTEGER NOT NULL CHECK(kind IN (0, 1)),
	size INTEGER NOT NULL CHECK(size >= 0),
	access INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS totals(
	id INTEGER PRIMARY KEY CHECK(id = 1),
	object_bytes INTEGER NOT NULL,
	entries INTEGER NOT NULL
);
INSERT OR IGNORE INTO totals(id, object_bytes, entries) VALUES(1, 0, 0);
CREATE TABLE IF NOT EXISTS meta(name TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
` + indexAccessDDL + indexTriggerDDL

// indexAccessDDL and indexTriggerDDL are apart from the tables so a bulk
// import can drop them and build them again after the load.
const indexAccessDDL = `
CREATE INDEX IF NOT EXISTS entries_by_kind_access ON entries(kind, access);
`

const indexTriggerDDL = `
CREATE TRIGGER IF NOT EXISTS entries_insert AFTER INSERT ON entries BEGIN
	UPDATE totals SET
		object_bytes = object_bytes + CASE WHEN NEW.kind = 0 THEN NEW.size ELSE 0 END,
		entries = entries + 1
	WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS entries_delete AFTER DELETE ON entries BEGIN
	UPDATE totals SET
		object_bytes = object_bytes - CASE WHEN OLD.kind = 0 THEN OLD.size ELSE 0 END,
		entries = entries - 1
	WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS entries_update AFTER UPDATE OF size, kind ON entries BEGIN
	UPDATE totals SET object_bytes = object_bytes
		- CASE WHEN OLD.kind = 0 THEN OLD.size ELSE 0 END
		+ CASE WHEN NEW.kind = 0 THEN NEW.size ELSE 0 END
	WHERE id = 1;
END;
`

// upsertEntry keeps the latest size and the latest access of a key. The
// access only moves forward, so a late hint never makes an entry look older.
const upsertEntry = `INSERT INTO entries(key, kind, size, access) VALUES(?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET kind = excluded.kind, size = excluded.size, access = max(entries.access, excluded.access)`

// indexEntry is one index row. Its JSON form is the legacy journal row.
type indexEntry struct {
	Key     string `json:"key"`
	Size    int64  `json:"size"`
	Access  int64  `json:"access"`
	Deleted bool   `json:"deleted,omitempty"`
}

const (
	kindObject = 0
	kindAction = 1
)

// entryKind validates a key and returns its kind. Only a valid key may name
// a file: an object key is 64 lowercase hex characters, and an action key
// is "action-" and the lowercase hex of a valid action ID.
func entryKind(key string) (int, bool) {
	raw, action := strings.CutPrefix(key, "action-")
	decoded, err := hex.DecodeString(raw)
	if err != nil || raw != strings.ToLower(raw) {
		return 0, false
	}
	if action {
		return kindAction, validID(decoded)
	}
	return kindObject, len(decoded) == 32
}

// openIndexDB opens the index with one connection. synchronous FULL makes a
// committed put durable before its object is published; hints use OFF.
func openIndexDB(path string, busy time.Duration, synchronous string) (*sql.DB, error) {
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	query.Add("_pragma", "synchronous("+synchronous+")")
	db, err := sql.Open("sqlite", "file:"+path+"?"+query.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// openIndexLocked opens the index for a client that changes the cache and
// imports journal rows that older clients appended. The caller holds the
// authority lock.
func (c *Cache) openIndexLocked() error {
	if c.db == nil {
		path := filepath.Join(c.dir, indexDBFile)
		// Create the file owner-only; SQLite gives its -wal and -shm
		// files the same mode.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		f.Close()
		db, err := openIndexDB(path, authorityLockTimeout, "FULL")
		if err != nil {
			return err
		}
		if err := ensureIndexSchema(db); err != nil {
			db.Close()
			return err
		}
		c.db = db
	}
	return c.importJournalLocked()
}

func ensureIndexSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == indexSchemaVer {
		return nil
	}
	if version > indexSchemaVer {
		return fmt.Errorf("cache index schema %d is newer than this binary (%d)", version, indexSchemaVer)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return err
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("cache index journal mode %q, want wal", mode)
	}
	if _, err := db.Exec(indexSchema); err != nil {
		return err
	}
	_, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", indexSchemaVer))
	return err
}

func readMeta(tx *sql.Tx, name string) (string, error) {
	var value string
	err := tx.QueryRow("SELECT value FROM meta WHERE name = ?", name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func writeMeta(tx *sql.Tx, name, value string) error {
	_, err := tx.Exec("INSERT INTO meta(name, value) VALUES(?, ?) ON CONFLICT(name) DO UPDATE SET value = excluded.value", name, value)
	return err
}

// importJournalLocked applies the legacy journal rows that this index has
// not seen. The journal is read from the recorded offset when its
// generation (first row) is unchanged; a rewritten journal (an old client's
// compaction) is read again from its start. Rows only add, refresh or delete
// entries, so a repeated row is harmless. A torn row, a row with an invalid
// identity and an incomplete final row are skipped: none of them can name a
// file, and the journal is not this reader's to repair.
func (c *Cache) importJournalLocked() error {
	path := filepath.Join(c.dir, legacyJournalFile)
	statInfo, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		c.journalSynced = false
		return nil
	}
	if err != nil {
		return err
	}
	if c.journalSynced && statInfo.Size() == c.journalSize && statInfo.ModTime().Equal(c.journalModTime) {
		return nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		c.journalSynced = false
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	gen, err := readMeta(tx, "journal_generation")
	if err != nil {
		return err
	}
	rawOffset, err := readMeta(tx, "journal_offset")
	if err != nil {
		return err
	}
	offset, _ := strconv.ParseInt(rawOffset, 10, 64)
	first, ok := peekJournalFirstKey(f)
	if !ok {
		return nil // empty, or a first row still being written
	}
	if first != gen || offset < 0 || offset > info.Size() || !rowBoundary(f, offset) {
		offset = 0
	}
	if offset == info.Size() {
		c.journalSynced = true
		c.journalSize = info.Size()
		c.journalModTime = info.ModTime()
		return nil
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	full := offset == 0
	live := map[string]indexEntry{}
	deleted := map[string]bool{}
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, e := r.ReadBytes('\n')
		if errors.Is(e, io.EOF) {
			break // an incomplete final row may still be in flight
		}
		if e != nil {
			return e
		}
		start := offset
		offset += int64(len(line))
		var row indexEntry
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		if _, valid := entryKind(row.Key); !valid || row.Size < 0 || row.Size > MaxObjectBytes || row.Access < 0 {
			continue
		}
		if start == 0 && row.Size == 0 && row.Access == 0 && !row.Deleted {
			continue // a compaction's generation marker
		}
		if row.Deleted {
			delete(live, row.Key)
			deleted[row.Key] = true
			continue
		}
		delete(deleted, row.Key)
		if old, ok := live[row.Key]; ok && old.Access > row.Access {
			row.Access = old.Access
		}
		live[row.Key] = row
	}
	if err = applyJournalRows(tx, live, deleted, full); err != nil {
		return err
	}
	if err = writeMeta(tx, "journal_generation", first); err != nil {
		return err
	}
	if err = writeMeta(tx, "journal_offset", strconv.FormatInt(offset, 10)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if offset == info.Size() {
		c.journalSynced = true
		c.journalSize = info.Size()
		c.journalModTime = info.ModTime()
	} else {
		c.journalSynced = false
	}
	return nil
}

// applyJournalRows writes the net effect of journal rows. A full import (a
// first migration of up to about a million rows, or a re-read after an old
// client compacted) loads in key order with the trigger and access index
// dropped, then recomputes the totals and builds both again: row-by-row
// trigger updates made the first import of an 860k-row journal take 21.6 s
// under the authority lock. A tail import is a few rows and uses the
// triggers. DDL is transactional in SQLite, so a failed load leaves the
// schema as it was.
func applyJournalRows(tx *sql.Tx, live map[string]indexEntry, deleted map[string]bool, full bool) error {
	if full {
		for _, name := range []string{"entries_insert", "entries_delete", "entries_update"} {
			if _, err := tx.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DROP INDEX IF EXISTS entries_by_kind_access"); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(live))
	for key := range live {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	upsert, err := tx.Prepare(upsertEntry)
	if err != nil {
		return err
	}
	defer upsert.Close()
	for _, key := range keys {
		row := live[key]
		kind, _ := entryKind(key)
		if _, err := upsert.Exec(key, kind, row.Size, row.Access); err != nil {
			return err
		}
	}
	remove, err := tx.Prepare("DELETE FROM entries WHERE key = ?")
	if err != nil {
		return err
	}
	defer remove.Close()
	for key := range deleted {
		if _, err := remove.Exec(key); err != nil {
			return err
		}
	}
	if !full {
		return nil
	}
	if _, err := tx.Exec(`UPDATE totals SET
		object_bytes = (SELECT COALESCE(SUM(size), 0) FROM entries WHERE kind = 0),
		entries = (SELECT COUNT(*) FROM entries)
		WHERE id = 1`); err != nil {
		return err
	}
	if _, err := tx.Exec(indexAccessDDL); err != nil {
		return err
	}
	_, err = tx.Exec(indexTriggerDDL)
	return err
}

// peekJournalFirstKey returns the generation of the journal's first row. It
// reads through ReadAt, so it does not disturb the caller's seek position.
func peekJournalFirstKey(f *os.File) (string, bool) {
	buf := make([]byte, 4097)
	n, err := f.ReadAt(buf, 0)
	if n == 0 || (err != nil && err != io.EOF) {
		return "", false
	}
	line := buf[:n]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	} else if n > 4096 {
		return "", false
	} else {
		return "", false // the first row is not complete yet
	}
	return journalGeneration(line), true
}

// journalGeneration names a journal by its first row: the row's key, or a
// digest of the raw row when it does not parse.
func journalGeneration(line []byte) string {
	line = bytes.TrimSuffix(line, []byte("\n"))
	var head struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(line, &head) == nil && head.Key != "" {
		return head.Key
	}
	sum := sha256.Sum256(line)
	return "raw:" + hex.EncodeToString(sum[:])
}

// rowBoundary reports whether offset starts a row.
func rowBoundary(f *os.File, offset int64) bool {
	if offset == 0 {
		return true
	}
	b := []byte{0}
	n, err := f.ReadAt(b, offset-1)
	return n == 1 && err == nil && b[0] == '\n'
}

// recordPut registers a put's object and action rows in one transaction.
func (c *Cache) recordPut(object indexEntry, action indexEntry) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, row := range []indexEntry{object, action} {
		kind, ok := entryKind(row.Key)
		if !ok {
			return fmt.Errorf("invalid cache index key")
		}
		if _, err = tx.Exec(upsertEntry, row.Key, kind, row.Size, row.Access); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// writeHints records get access times in one transaction.
func writeHints(db *sql.DB, hints map[string]indexEntry) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(upsertEntry)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range hints {
		if _, err = stmt.Exec(row.Key, kindObject, row.Size, row.Access); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// flushHintsUnlocked records a get-only client's hints without the
// authority lock. It needs an index that a put client created; without one
// the hints are dropped.
func (c *Cache) flushHintsUnlocked() {
	if len(c.hints) == 0 {
		return
	}
	path := filepath.Join(c.dir, indexDBFile)
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return
	}
	db, err := openIndexDB(path, hintBusyTimeout, "OFF")
	if err != nil {
		return
	}
	defer db.Close()
	_ = writeHints(db, c.hints)
}

func (c *Cache) indexTotals() (objectBytes, entries int64, err error) {
	err = c.db.QueryRow("SELECT object_bytes, entries FROM totals WHERE id = 1").Scan(&objectBytes, &entries)
	return objectBytes, entries, err
}

type candidate struct {
	indexEntry
	kind int
}

func (c *Cache) needsEvictLocked() bool {
	total, count, err := c.indexTotals()
	return err == nil && (total > c.maxBytes || count > c.maxEntries())
}

func (c *Cache) readEvictStamp() int64 {
	if c.evictLock == nil {
		return 0
	}
	var buf [64]byte
	n, err := c.evictLock.ReadAt(buf[:], 0)
	if n == 0 || (err != nil && !errors.Is(err, io.EOF)) {
		return 0
	}
	v, parseErr := strconv.ParseInt(strings.TrimSpace(string(buf[:n])), 10, 64)
	if parseErr != nil || v <= 0 {
		return 0
	}
	return v
}

func (c *Cache) writeEvictStamp(ns int64) {
	if c.evictLock == nil {
		return
	}
	raw := []byte(strconv.FormatInt(ns, 10) + "\n")
	_ = c.evictLock.Truncate(0)
	_, _ = c.evictLock.WriteAt(raw, 0)
}

func (c *Cache) evictInCooldown(now int64) bool {
	if defaultPutEvictCooldown <= 0 || c.putsSinceEvict >= defaultPutEvictBytes {
		return false
	}
	last := c.readEvictStamp()
	return last > 0 && now-last < int64(defaultPutEvictCooldown)
}

// tryEvict acquires the non-blocking singleton evict.lock before taking the
// authority lock so at most one gocache-lru process trims the cache at a time.
// Concurrent processes skip trimming cleanly instead of competing over locks
// and SQLite writes. When force is false (mid-build puts), a short shared
// cooldown also suppresses back-to-back eviction storms when remaining objects
// are pinned or inside the grace window.
func (c *Cache) tryEvict(force bool) error {
	now := time.Now().UnixNano()
	if !force && c.evictInCooldown(now) {
		c.evictSkippedCooldown++
		return nil
	}
	if c.evictLock != nil {
		if err := unix.Flock(int(c.evictLock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
				c.evictSkippedBusy++
				return nil
			}
			return err
		}
		defer unix.Flock(int(c.evictLock.Fd()), unix.LOCK_UN)
		if !force && c.evictInCooldown(time.Now().UnixNano()) {
			c.evictSkippedCooldown++
			return nil
		}
	}
	err := c.withLock(func() error {
		if err := c.openIndexLocked(); err != nil {
			return err
		}
		return c.evictLocked()
	})
	if err == nil {
		c.putsSinceEvict = 0
		c.writeEvictStamp(time.Now().UnixNano())
	}
	return err
}

func (c *Cache) oldest(kind, limit int, maxAccess int64) ([]candidate, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if maxAccess > 0 {
		rows, err = c.db.Query("SELECT key, size, access FROM entries WHERE kind = ? AND access <= ? ORDER BY access LIMIT ?", kind, maxAccess, limit)
	} else {
		rows, err = c.db.Query("SELECT key, size, access FROM entries WHERE kind = ? ORDER BY access LIMIT ?", kind, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		row := candidate{kind: kind}
		if err := rows.Scan(&row.Key, &row.Size, &row.Access); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (c *Cache) maxEntries() int64 {
	n := c.maxBytes / 4096
	if n < 256 {
		n = 256
	}
	if n > 1000000 {
		n = 1000000
	}
	return n
}

// evictLocked removes the least recently used entries while the cache is
// over its byte target or its entry cap. Objects go first by access time;
// action records are candidates only while the entry cap is exceeded. A
// pass examines at most evictionBudget candidates. Pinned objects (another
// client holds a link) and objects inside the grace window stay; a pinned
// object is demoted for a second so later candidates are reached. Deletes and
// durable eviction counters commit in one SQLite transaction instead of one
// fsync per removed key. The caller holds the authority lock.
func (c *Cache) evictLocked() error {
	total, count, err := c.indexTotals()
	if err != nil {
		return err
	}
	over := func() bool { return total > c.maxBytes || count > c.maxEntries() }
	if !over() {
		return nil
	}
	now := time.Now().UnixNano()
	for key, until := range c.retry {
		if until <= now {
			delete(c.retry, key)
		}
	}
	limit := evictionBudget + len(c.retry)
	objMaxAccess := int64(0)
	if c.graceWindow > 0 && now > int64(c.graceWindow) {
		objMaxAccess = now - int64(c.graceWindow)
	}
	candidates, err := c.oldest(kindObject, limit, objMaxAccess)
	if err != nil {
		return err
	}
	if count > c.maxEntries() {
		actions, err := c.oldest(kindAction, limit, 0)
		if err != nil {
			return err
		}
		candidates = append(candidates, actions...)
	}
	// A skipped pinned candidate is demoted, not excluded: its priority is
	// its retry time, so unpinned rows behind it come first, and it is
	// examined again once the rows ahead of it are spent.
	priority := func(row candidate) int64 {
		if until := c.retry[row.Key]; until > row.Access {
			return until
		}
		return row.Access
	}
	sort.SliceStable(candidates, func(i, j int) bool { return priority(candidates[i]) < priority(candidates[j]) })
	scanned := 0
	var forgotten []string
	var reclaimedBytes int64
	for _, row := range candidates {
		if !over() || scanned >= evictionBudget {
			break
		}
		scanned++
		kind, valid := entryKind(row.Key)
		if !valid || kind != row.kind {
			// A corrupt row never names a file: forget it.
			forgotten = append(forgotten, row.Key)
			count--
			if row.kind == kindObject {
				total -= row.Size
			}
			continue
		}
		if kind == kindAction && count <= c.maxEntries() {
			continue
		}
		if kind == kindObject && c.graceWindow > 0 && row.Access > 0 && now-row.Access < int64(c.graceWindow) {
			continue
		}
		path := c.objectPath(row.Key)
		info, err := os.Stat(path)
		if err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
				c.retry[row.Key] = now + int64(time.Second)
				continue
			}
			if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		forgotten = append(forgotten, row.Key)
		count--
		if kind == kindObject {
			total -= row.Size
			reclaimedBytes += row.Size
		}
	}
	if len(forgotten) == 0 && c.evictSkippedBusy == 0 && c.evictSkippedCooldown == 0 {
		return nil
	}
	return c.commitEvictionLocked(forgotten, reclaimedBytes, now)
}

func addMetaCounter(tx *sql.Tx, name string, delta int64) error {
	if delta == 0 {
		return nil
	}
	raw, err := readMeta(tx, name)
	if err != nil {
		return err
	}
	current, _ := strconv.ParseInt(raw, 10, 64)
	return writeMeta(tx, name, strconv.FormatInt(current+delta, 10))
}

func (c *Cache) commitEvictionLocked(keys []string, reclaimedBytes, now int64) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(keys) > 0 {
		stmt, err := tx.Prepare("DELETE FROM entries WHERE key = ?")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, key := range keys {
			if _, err := stmt.Exec(key); err != nil {
				return err
			}
		}
	}
	if err := writeMeta(tx, "last_evict_at", strconv.FormatInt(now, 10)); err != nil {
		return err
	}
	if err := addMetaCounter(tx, "evict_runs_total", 1); err != nil {
		return err
	}
	if err := addMetaCounter(tx, "evict_removed_total", int64(len(keys))); err != nil {
		return err
	}
	if err := addMetaCounter(tx, "evict_reclaimed_bytes_total", reclaimedBytes); err != nil {
		return err
	}
	if err := addMetaCounter(tx, "evict_skipped_busy_total", c.evictSkippedBusy); err != nil {
		return err
	}
	if err := addMetaCounter(tx, "evict_skipped_cooldown_total", c.evictSkippedCooldown); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.evictSkippedBusy = 0
	c.evictSkippedCooldown = 0
	return nil
}
