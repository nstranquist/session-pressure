package gocachelru

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// runner-3, 2026-11-15: every go command starts a helper, and each
// helper replayed a 1.1 M-row journal and took the authority lock (with an
// fsync) for every get. These tests pin the lock-free get and the lazy index.

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

func appendJournal(t *testing.T, dir, raw string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "index.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
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

// A row with valid syntax but an invalid identity hard-fails any replay
// (the escape guard). Startup, get and a get-only close never replay, so
// such a row cannot stop a warm build; only a put meets it, and a put
// degrades to an unindexed object.
func TestStartupGetAndGetOnlyCloseNeverReplay(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, "warm")
	appendJournal(t, dir, `{"key":"../escape","size":1,"access":1}`+"\n")
	c, err := New(dir, 1<<30)
	if err != nil {
		t.Fatalf("New replayed the journal: %v", err)
	}
	if got := c.get(Request{ID: 1, ActionID: []byte("warm")}); got.Miss || got.Size != 3 {
		t.Fatalf("get = %+v", got)
	}
	if c.indexed || len(c.index) != 0 {
		t.Fatalf("a get loaded the index: indexed=%v rows=%d", c.indexed, len(c.index))
	}
	if err := c.Close(); err != nil {
		t.Fatalf("get-only close replayed the journal: %v", err)
	}
	if _, err := os.Stat(c.pins); !os.IsNotExist(err) {
		t.Fatalf("get-only close left its pins: %v", err)
	}
	p, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	put := p.put(Request{ID: 2, ActionID: []byte("cold"), OutputID: []byte("o"), BodySize: 2}, []byte("xy"))
	if put.Err != "" || put.DiskPath == "" {
		t.Fatalf("put on a bad journal must degrade, not fail: %+v", put)
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

// Lock-free hint rows from one client land between another client's locked
// rows. The putting client must stay row-aligned and agree with a fresh
// full replay.
func TestHintRowsKeepPutClientsAligned(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, "warm")
	reader, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for i := 0; i < 20; i++ {
		if got := reader.get(Request{ID: int64(i), ActionID: []byte("warm")}); got.Miss {
			t.Fatal("warm miss")
		}
		id := []byte{'w', byte(i)}
		if r := writer.put(Request{ID: int64(100 + i), ActionID: id, OutputID: id, BodySize: 1}, []byte("x")); r.Err != "" {
			t.Fatal(r.Err)
		}
	}
	// A torn hint (a killed lock-free append) before the next locked row.
	appendJournal(t, dir, `{"key":"torn`)
	if r := writer.put(Request{ID: 200, ActionID: []byte("last"), OutputID: []byte("o"), BodySize: 1}, []byte("x")); r.Err != "" {
		t.Fatal(r.Err)
	}
	if err := writer.withLock(writer.loadIndex); err != nil {
		t.Fatalf("writer replay after hints: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if writer.offset != info.Size() {
		t.Fatalf("writer offset %d, journal %d", writer.offset, info.Size())
	}
	fresh := &Cache{dir: dir, index: map[string]indexEntry{}}
	if err := fresh.replayJournal(false); err != nil {
		t.Fatal(err)
	}
	if fresh.total != writer.total || len(fresh.index) != len(writer.index) {
		t.Fatalf("writer total %d rows %d, fresh replay total %d rows %d", writer.total, len(writer.index), fresh.total, len(fresh.index))
	}
	if _, ok := fresh.index["action-"+hex.EncodeToString([]byte("last"))]; !ok {
		t.Fatal("the row after the torn hint was lost")
	}
}

// The runner-3 failure of 2026-11-15: a client whose offset survived a
// cut of the journal resumed mid-row after peers regrew it, read a broken
// row, and truncated the shared journal at its own offset, deleting peers'
// rows. Nothing may cut a journal row another client wrote.
func TestStaleOffsetNeverCutsPeerRows(t *testing.T) {
	dir := t.TempDir()
	a, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if r := a.put(Request{ID: 1, ActionID: []byte("first"), OutputID: []byte("o"), BodySize: 1}, []byte("x")); r.Err != "" {
		t.Fatal(r.Err)
	}
	journal := filepath.Join(dir, "index.jsonl")
	info, err := os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	// A peer cut the journal 10 bytes into a's last row (a's offset is now
	// past the end), then other clients regrow it past a's offset.
	if err := os.Truncate(journal, info.Size()-10); err != nil {
		t.Fatal(err)
	}
	b, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := 0; i < 3; i++ {
		id := []byte{'p', byte('0' + i)}
		if r := b.put(Request{ID: int64(10 + i), ActionID: id, OutputID: id, BodySize: 1}, []byte("y")); r.Err != "" {
			t.Fatal(r.Err)
		}
	}
	if r := a.put(Request{ID: 2, ActionID: []byte("second"), OutputID: []byte("o"), BodySize: 1}, []byte("z")); r.Err != "" {
		t.Fatal(r.Err)
	}
	fresh := &Cache{dir: dir, index: map[string]indexEntry{}}
	if err := fresh.loadIndex(); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"p0", "p1", "p2", "second"} {
		if _, ok := fresh.index["action-"+hex.EncodeToString([]byte(action))]; !ok {
			t.Fatalf("journal lost %s's row", action)
		}
	}
}
