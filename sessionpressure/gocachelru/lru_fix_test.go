package gocachelru

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression test for the gaterunner-3 failures ("invalid cache index:
// invalid character 'k'"): a compaction rewrote the journal, the inode
// number was recycled by the replacement file, and a live client whose
// recorded inode matched resumed replay at a stale offset mid-row. The
// journal's first-row key is its generation; a rewrite must be detected
// from content even when inode and size both look continuous.
func TestJournalRewriteDetectedDespiteSameInode(t *testing.T) {
	dirA := t.TempDir()
	a, err := New(dirA, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	body := bytes.Repeat([]byte("a"), 1_000_000)
	put := a.put(Request{ID: 1, ActionID: []byte("action-a"), OutputID: []byte("out-a"), BodySize: int64(len(body))}, body)
	if put.Err != "" {
		t.Fatal(put.Err)
	}
	if got := a.get(Request{ID: 2, ActionID: []byte("action-a")}); got.Miss || got.Err != "" {
		t.Fatalf("seed get: %+v", got)
	}

	// Build a different, larger journal in a second cache directory.
	dirB := t.TempDir()
	b, err := New(dirB, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		id := []byte{byte('x'), byte(i)}
		r := b.put(Request{ID: int64(i + 1), ActionID: id, OutputID: id, BodySize: 10}, bytes.Repeat([]byte("b"), 10))
		if r.Err != "" {
			t.Fatal(r.Err)
		}
		if got := b.get(Request{ID: int64(10 + i), ActionID: id}); got.Miss {
			t.Fatalf("seed get B: %+v", got)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	rawB, err := os.ReadFile(filepath.Join(dirB, "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(rawB)) <= a.offset {
		t.Fatalf("journal B (%d bytes) must exceed A's offset (%d) for this repro", len(rawB), a.offset)
	}
	// Rewrite A's journal in place: same inode, larger size. Neither the
	// inode check nor the size check can catch this; the generation can.
	journalA := filepath.Join(dirA, "index.jsonl")
	before, err := os.Stat(journalA)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(journalA, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(rawB); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(journalA)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("in-place rewrite must keep the inode for this repro")
	}

	// Journal B has no row for A's action, but the action metadata file
	// still lives in dirA and remains authoritative for reads, so the
	// get may legitimately hit. What it must never do is error or serve
	// wrong bytes (the old code returned "invalid cache index" here).
	got := a.get(Request{ID: 3, ActionID: []byte("action-a")})
	if got.Err != "" {
		t.Fatalf("get after rewrite errored: %+v", got)
	}
	if !got.Miss {
		raw, err := os.ReadFile(got.DiskPath)
		if err != nil || len(raw) != len(body) || !bytes.Equal(raw, body) {
			t.Fatalf("get after rewrite served wrong content: %d bytes, %v", len(raw), err)
		}
	}
	// A can still write, and a fresh client replays the whole journal.
	r := a.put(Request{ID: 4, ActionID: []byte("action-new"), OutputID: []byte("out-new"), BodySize: 3}, []byte("new"))
	if r.Err != "" {
		t.Fatalf("put after rewrite: %s", r.Err)
	}
	c, err := New(dirA, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := c.get(Request{ID: 5, ActionID: []byte("action-new")}); got.Miss || got.Size != 3 {
		t.Fatalf("fresh client lost post-rewrite put: %+v", got)
	}
}

// A torn or garbage tail must be dropped, not fail the cache: the file on
// gaterunner-3 parsed clean while live readers were failing, so index
// damage is a reader-side hazard and recovery belongs in replay.
func TestTornJournalTailRecovered(t *testing.T) {
	dir := t.TempDir()
	a, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	r := a.put(Request{ID: 1, ActionID: []byte("keep"), OutputID: []byte("out"), BodySize: 3}, []byte("abc"))
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, "index.jsonl")
	good, err := os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("kkkk not json\n{\"key\":\"torn"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := New(dir, 1<<30)
	if err != nil {
		t.Fatalf("torn tail must not fail New: %v", err)
	}
	defer b.Close()
	// Truncation happens during New's replay, before any new rows are
	// recorded by the gets below.
	info, err := os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != good.Size() {
		t.Fatalf("journal not truncated to last good row: %d != %d", info.Size(), good.Size())
	}
	if got := b.get(Request{ID: 2, ActionID: []byte("keep")}); got.Miss || got.Size != 3 {
		t.Fatalf("entry before torn tail lost: %+v", got)
	}
}

// Under lock contention a get degrades to a miss and a put serves its
// already-written object unindexed; neither may error into the build.
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
		if got.Err != "" || !got.Miss {
			t.Errorf("contended get = %+v, want clean miss", got)
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

// Compaction must rewrite rarely and mark the new journal generation, so
// a long journal of access rows collapses to the live set and other
// clients can tell the file changed.
func TestCompactionMarksGenerationAndShrinks(t *testing.T) {
	old := compactionMinBytes
	compactionMinBytes = 1
	defer func() { compactionMinBytes = old }()

	dir := t.TempDir()
	c, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	r := c.put(Request{ID: 1, ActionID: []byte("one"), OutputID: []byte("out"), BodySize: 3}, []byte("abc"))
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	for i := 0; i < 60; i++ {
		if got := c.get(Request{ID: int64(10 + i), ActionID: []byte("one")}); got.Miss {
			t.Fatalf("get %d missed", i)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) > 4 {
		t.Fatalf("compacted journal has %d lines, want <= 4", len(lines))
	}
	var marker indexEntry
	if err := json.Unmarshal(lines[0], &marker); err != nil {
		t.Fatal(err)
	}
	if marker.Size != 0 || marker.Access != 0 || len(marker.Key) != 64 {
		t.Fatalf("first row is not a generation marker: %+v", marker)
	}
	next, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if got := next.get(Request{ID: 2, ActionID: []byte("one")}); got.Miss || got.Size != 3 {
		t.Fatalf("entry lost across compaction: %+v", got)
	}
}

// A peer closing (and compacting) mid-flight must not disturb a live
// client's next operations.
func TestPeerCloseCompactionDoesNotDisturbLiveClient(t *testing.T) {
	old := compactionMinBytes
	compactionMinBytes = 1
	defer func() { compactionMinBytes = old }()

	dir := t.TempDir()
	a, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := New(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	r := b.put(Request{ID: 1, ActionID: []byte("via-b"), OutputID: []byte("out"), BodySize: 3}, []byte("abc"))
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	for i := 0; i < 40; i++ {
		if got := b.get(Request{ID: int64(10 + i), ActionID: []byte("via-b")}); got.Miss {
			t.Fatalf("get %d missed", i)
		}
	}
	if err := b.Close(); err != nil { // compacts the journal under A
		t.Fatal(err)
	}
	if got := a.get(Request{ID: 2, ActionID: []byte("via-b")}); got.Miss || got.Err != "" || got.Size != 3 {
		t.Fatalf("live client after peer compaction: %+v", got)
	}
	r = a.put(Request{ID: 3, ActionID: []byte("via-a"), OutputID: []byte("out"), BodySize: 3}, []byte("xyz"))
	if r.Err != "" {
		t.Fatalf("live client put after peer compaction: %s", r.Err)
	}
}
