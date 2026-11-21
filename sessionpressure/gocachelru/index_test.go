package gocachelru

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The SQLite index (WORK-1601). A put no longer replays a journal, a get
// never opens the index, and older binaries that still append to
// index.jsonl stay accounted for through the journal import.

func seedCache(t *testing.T, dir string, actions ...string) {
	t.Helper()
	c, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	for i, action := range actions {
		if r := c.put(Request{ID: int64(i + 1), ActionID: []byte(action), OutputID: []byte(action), BodySize: 3}, []byte("abc")); r.Err != "" {
			t.Fatal(r.Err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func openTestIndex(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := openIndexDB(filepath.Join(dir, indexDBFile), time.Second, "FULL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// indexRows returns key -> row for every entry, and checks that the trigger
// totals agree with the rows.
func indexRows(t *testing.T, dir string) map[string]indexEntry {
	t.Helper()
	db := openTestIndex(t, dir)
	rows, err := db.Query("SELECT key, kind, size, access FROM entries")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]indexEntry{}
	var objectBytes, entries int64
	for rows.Next() {
		var row indexEntry
		var kind int
		if err := rows.Scan(&row.Key, &kind, &row.Size, &row.Access); err != nil {
			t.Fatal(err)
		}
		if kind == kindObject {
			objectBytes += row.Size
		}
		entries++
		out[row.Key] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var totalBytes, totalEntries int64
	if err := db.QueryRow("SELECT object_bytes, entries FROM totals WHERE id = 1").Scan(&totalBytes, &totalEntries); err != nil {
		t.Fatal(err)
	}
	if totalBytes != objectBytes || totalEntries != entries {
		t.Fatalf("totals %d bytes / %d entries, rows %d / %d", totalBytes, totalEntries, objectBytes, entries)
	}
	return out
}

func journalRow(t *testing.T, row indexEntry) string {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw) + "\n"
}

func appendJournal(t *testing.T, dir, raw string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, legacyJournalFile), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func objectKey(i int) string { return fmt.Sprintf("%064x", i) }

func putOne(t *testing.T, c *Cache, id int64, action, body string) Response {
	t.Helper()
	r := c.put(Request{ID: id, ActionID: []byte(action), OutputID: []byte("o-" + action), BodySize: int64(len(body))}, []byte(body))
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	return r
}

// A get and a get-only close never open the index under the authority
// lock: a warm build's helpers stay lock-free even while a peer holds it,
// and their access hints still reach the index.
func TestGetOnlyClientsNeverTakeTheAuthorityLock(t *testing.T) {
	old := authorityLockTimeout
	authorityLockTimeout = 5 * time.Second
	defer func() { authorityLockTimeout = old }()
	dir := t.TempDir()
	seedCache(t, dir, "warm")
	before := indexRows(t, dir)
	c, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	err = holder.withLock(func() error {
		started := time.Now()
		got := c.get(Request{ID: 1, ActionID: []byte("warm")})
		if got.Miss || got.Size != 3 {
			t.Errorf("get = %+v", got)
		}
		if c.indexed || c.db != nil {
			t.Error("a get opened the index")
		}
		if err := c.Close(); err != nil {
			t.Errorf("get-only close: %v", err)
		}
		if waited := time.Since(started); waited > time.Second {
			t.Errorf("get and close waited %v under a held lock", waited)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.pins); !os.IsNotExist(err) {
		t.Fatalf("get-only close left its pins: %v", err)
	}
	sum := sha256.Sum256([]byte("abc"))
	object := hex.EncodeToString(sum[:])
	if got := indexRows(t, dir)[object]; got.Access <= before[object].Access {
		t.Fatalf("hint did not advance the object: %d <= %d", got.Access, before[object].Access)
	}
}

// A put keeps the index exact: object and action rows, trigger totals, and
// hints that only move access forward.
func TestPutRecordsObjectAndActionRows(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	r := putOne(t, c, 1, "first", "hello")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	rows := indexRows(t, dir)
	object := filepath.Base(r.DiskPath)
	if got := rows[object]; got.Size != 5 {
		t.Fatalf("object row = %+v", got)
	}
	action := "action-" + hex.EncodeToString([]byte("first"))
	if got, ok := rows[action]; !ok || got.Size == 0 {
		t.Fatalf("action row = %+v, %v", got, ok)
	}
	// A late hint carries an older access; it must not move the row back.
	db := openTestIndex(t, dir)
	if err := writeHints(db, map[string]indexEntry{object: {Key: object, Size: 5, Access: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := indexRows(t, dir)[object]; got.Access != rows[object].Access {
		t.Fatalf("old hint moved access back: %d -> %d", rows[object].Access, got.Access)
	}
}

// The first new client imports the legacy journal; later puts import only
// the rows that old clients appended, and a journal that an old client
// compacted is read again without losing rows that only the index holds.
func TestLegacyJournalImportOnceThenTail(t *testing.T) {
	dir := t.TempDir()
	for _, part := range []string{"objects", "actions", "clients"} {
		if err := os.MkdirAll(filepath.Join(dir, part), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	appendJournal(t, dir, journalRow(t, indexEntry{Key: objectKey(1), Size: 10, Access: 100})+
		journalRow(t, indexEntry{Key: objectKey(2), Size: 20, Access: 200})+
		"{\"key\":torn\n"+
		journalRow(t, indexEntry{Key: "../escape", Size: 1, Access: 1})+
		journalRow(t, indexEntry{Key: "action-" + hex.EncodeToString([]byte("a")), Size: 7, Access: 300})+
		journalRow(t, indexEntry{Key: objectKey(2), Deleted: true})+
		journalRow(t, indexEntry{Key: objectKey(1), Size: 10, Access: 150})+
		`{"key":"`+objectKey(9)) // incomplete final row: still in flight
	c, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	put := putOne(t, c, 1, "new", "xyz")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	rows := indexRows(t, dir)
	if got := rows[objectKey(1)]; got.Size != 10 || got.Access != 150 {
		t.Fatalf("imported row = %+v", got)
	}
	if _, ok := rows[objectKey(2)]; ok {
		t.Fatal("a deleted journal row was imported")
	}
	if _, ok := rows["../escape"]; ok {
		t.Fatal("an invalid identity was imported")
	}
	if _, ok := rows[objectKey(9)]; ok {
		t.Fatal("an incomplete final row was imported")
	}
	newObject := filepath.Base(put.DiskPath)
	if _, ok := rows[newObject]; !ok {
		t.Fatal("the put's own row is missing")
	}

	// An old client completes its row and appends more.
	appendJournal(t, dir, `","size":90,"access":900}`+"\n"+journalRow(t, indexEntry{Key: objectKey(3), Size: 30, Access: 300}))
	c, err = New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	putOne(t, c, 2, "second", "abcd")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	rows = indexRows(t, dir)
	if rows[objectKey(9)].Size != 90 || rows[objectKey(3)].Size != 30 {
		t.Fatalf("tail rows not imported: %+v %+v", rows[objectKey(9)], rows[objectKey(3)])
	}

	// An old client compacts: a new marker row, then its own view. The
	// index keeps the new client's rows and drops what the old client
	// deleted afterwards.
	compacted := journalRow(t, indexEntry{Key: objectKey(0xabc)}) +
		journalRow(t, indexEntry{Key: objectKey(1), Size: 10, Access: 150}) +
		journalRow(t, indexEntry{Key: objectKey(3), Deleted: true})
	if err := os.WriteFile(filepath.Join(dir, legacyJournalFile), []byte(compacted), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	putOne(t, c, 3, "third", "q")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	rows = indexRows(t, dir)
	if _, ok := rows[objectKey(3)]; ok {
		t.Fatal("a delete after compaction was not imported")
	}
	if _, ok := rows[objectKey(0xabc)]; ok {
		t.Fatal("the compaction marker was imported as an entry")
	}
	if _, ok := rows[newObject]; !ok {
		t.Fatal("re-import dropped a row that only the index holds")
	}
}

// An index row or journal row that names a path outside the cache never
// reaches the filesystem: import skips it, and eviction forgets it.
func TestCorruptIndexCannotEscapeCache(t *testing.T) {
	prevGrace := defaultEvictGraceWindow
	defaultEvictGraceWindow = 0
	t.Cleanup(func() { defaultEvictGraceWindow = prevGrace })
	root := t.TempDir()
	dir := filepath.Join(root, "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	appendJournal(t, dir, journalRow(t, indexEntry{Key: "../outside", Size: 1, Access: 1}))
	c, err := New(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	put := putOne(t, c, 1, "a", "xy")
	if filepath.Dir(put.DiskPath) != filepath.Join(dir, "objects") {
		t.Fatalf("put degraded on a bad journal row: %+v", put)
	}
	// Plant corrupt rows directly; the sizes push the cache over target.
	if _, err := c.db.Exec("INSERT INTO entries(key, kind, size, access) VALUES('../../outside', 0, 100, 1), ('action-../../outside', 1, 1, 1)"); err != nil {
		t.Fatal(err)
	}
	if err := c.withLock(c.evictLocked); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "keep" {
		t.Fatalf("eviction escaped the cache: %q %v", raw, err)
	}
	rows := indexRows(t, dir)
	if _, ok := rows["../../outside"]; ok {
		t.Fatal("eviction kept the corrupt row")
	}
}

// More than one budget of old pinned objects precedes a reclaimable
// object. The next pass of the same client must reach it.
func TestPinnedCandidatesDoNotStarveLaterEviction(t *testing.T) {
	prevGrace := defaultEvictGraceWindow
	defaultEvictGraceWindow = 0
	t.Cleanup(func() { defaultEvictGraceWindow = prevGrace })
	dir := t.TempDir()
	c, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.withLock(c.openIndexLocked); err != nil {
		t.Fatal(err)
	}
	c.indexed = true
	hints := map[string]indexEntry{}
	for i := 0; i < 300; i++ {
		key := objectKey(i)
		if err := os.WriteFile(c.objectPath(key), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.pin(key); err != nil {
			t.Fatal(err)
		}
		hints[key] = indexEntry{Key: key, Size: 1, Access: int64(i + 1)}
	}
	free := objectKey(1000)
	if err := os.WriteFile(c.objectPath(free), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	hints[free] = indexEntry{Key: free, Size: 1, Access: 1000}
	if err := writeHints(c.db, hints); err != nil {
		t.Fatal(err)
	}
	c.maxBytes = 1
	for i := 0; i < 2; i++ {
		if err := c.withLock(c.evictLocked); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(c.objectPath(free)); !os.IsNotExist(err) {
		t.Fatalf("reclaimable object starved: %v", err)
	}
	for i := 0; i < 300; i++ {
		if _, err := os.Stat(c.objectPath(objectKey(i))); err != nil {
			t.Fatalf("pinned object %d removed: %v", i, err)
		}
	}
}

// Over the byte target, the oldest unpinned object goes first and action
// records stay; over the entry cap, action records go too.
func TestEvictionOrderAndEntryCap(t *testing.T) {
	prevGrace := defaultEvictGraceWindow
	defaultEvictGraceWindow = 0
	t.Cleanup(func() { defaultEvictGraceWindow = prevGrace })
	dir := t.TempDir()
	c, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	old := putOne(t, c, 1, "old", "aaaa")
	time.Sleep(2 * time.Millisecond)
	young := putOne(t, c, 2, "young", "bbbb")
	if err := c.Close(); err != nil { // drops this client's pins
		t.Fatal(err)
	}
	c, err = New(dir, 6) // room for one 4-byte object
	if err != nil {
		t.Fatal(err)
	}
	if err := c.withLock(func() error {
		if err := c.openIndexLocked(); err != nil {
			return err
		}
		return c.evictLocked()
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.DiskPath); !os.IsNotExist(err) {
		t.Fatalf("oldest object kept: %v", err)
	}
	if _, err := os.Stat(young.DiskPath); err != nil {
		t.Fatalf("younger object removed: %v", err)
	}
	rows := indexRows(t, dir)
	if _, ok := rows["action-"+hex.EncodeToString([]byte("old"))]; !ok {
		t.Fatal("an action record was evicted under the byte target alone")
	}
	// Force the entry cap: 256 entries minimum, so plant 300 action rows.
	hints := map[string]indexEntry{}
	for i := 0; i < 300; i++ {
		key := "action-" + hex.EncodeToString([]byte(fmt.Sprintf("bulk-%03d", i)))
		hints[key] = indexEntry{Key: key, Size: 1, Access: int64(i + 1)}
	}
	tx, err := c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range hints {
		if _, err := tx.Exec(upsertEntry, row.Key, kindAction, row.Size, row.Access); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := c.withLock(c.evictLocked); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if n := int64(len(indexRows(t, dir))); n > (&Cache{maxBytes: 6}).maxEntries() {
		t.Fatalf("entry cap not enforced: %d entries", n)
	}
}

// A client that puts and then gets: its close writes hints and evicts under
// the lock, and a second client sees the same object.
func TestPutThenGetAcrossClients(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	putOne(t, c, 1, "shared", "payload")
	if got := c.get(Request{ID: 2, ActionID: []byte("shared")}); got.Miss {
		t.Fatal("own put missed")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	got := next.get(Request{ID: 3, ActionID: []byte("shared")})
	if got.Miss {
		t.Fatal("second client missed")
	}
	raw, err := os.ReadFile(got.DiskPath)
	if err != nil || !bytes.Equal(raw, []byte("payload")) {
		t.Fatalf("object = %q, %v", raw, err)
	}
}

// A get holds no authority lock: it hits at once while a peer holds it,
// where it used to wait out the lock timeout and miss.
func TestGetHitsWhileAPeerHoldsTheLock(t *testing.T) {
	old := authorityLockTimeout
	authorityLockTimeout = 5 * time.Second
	defer func() { authorityLockTimeout = old }()
	dir := t.TempDir()
	seedCache(t, dir, "warm")
	c, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	holder, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	err = holder.withLock(func() error {
		started := time.Now()
		got := c.get(Request{ID: 1, ActionID: []byte("warm")})
		if got.Miss || time.Since(started) > time.Second {
			t.Errorf("get under a held lock: %+v after %v", got, time.Since(started))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A get needs no authority lock, so it hits while a peer holds it. A put
// under contention serves its already-written object unindexed; neither may
// error into the build.
func TestGetPutFailSoftUnderLockContention(t *testing.T) {
	old := authorityLockTimeout
	authorityLockTimeout = 150 * time.Millisecond
	defer func() { authorityLockTimeout = old }()

	dir := t.TempDir()
	seed, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	r := seed.put(Request{ID: 1, ActionID: []byte("seeded"), OutputID: []byte("out"), BodySize: 3}, []byte("abc"))
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	victim, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()
	holder, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	err = holder.withLock(func() error {
		got := victim.get(Request{ID: 2, ActionID: []byte("seeded")})
		if got.Err != "" || got.Miss || got.Size != 3 {
			t.Errorf("get under a held lock = %+v, want a hit", got)
		}
		put := victim.put(Request{ID: 3, ActionID: []byte("fresh"), OutputID: []byte("out2"), BodySize: 4}, []byte("data"))
		if put.Err != "" {
			t.Errorf("contended put errored: %s", put.Err)
		}
		raw, err := os.ReadFile(put.DiskPath)
		if err != nil || string(raw) != "data" {
			t.Errorf("contended put path = %q, %v", raw, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Once the lock frees, the cache works normally again.
	if got := victim.get(Request{ID: 4, ActionID: []byte("seeded")}); got.Miss || got.Size != 3 {
		t.Fatalf("get after contention: %+v", got)
	}
}
