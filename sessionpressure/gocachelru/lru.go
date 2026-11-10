// Package gocachelru implements GOCACHEPROG with immutable objects and a soft
// size target. Files returned to a Go process stay pinned until close or crash.
package gocachelru

import (
	"bufio"
	"bytes"
	"container/heap"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const DefaultMaxBytes = 16 << 30
const MaxObjectBytes = 1 << 30
const maxRequestBytes = 64 << 10

type Request struct {
	ID       int64  `json:"ID"`
	Command  string `json:"Command"`
	ActionID []byte `json:"ActionID,omitempty"`
	OutputID []byte `json:"OutputID,omitempty"`
	BodySize int64  `json:"BodySize,omitempty"`
}
type Response struct {
	ID            int64      `json:"ID"`
	Err           string     `json:"Err,omitempty"`
	KnownCommands []string   `json:"KnownCommands,omitempty"`
	Miss          bool       `json:"Miss,omitempty"`
	OutputID      []byte     `json:"OutputID,omitempty"`
	Size          int64      `json:"Size,omitempty"`
	Time          *time.Time `json:"Time,omitempty"`
	DiskPath      string     `json:"DiskPath,omitempty"`
}
type objectMeta struct {
	OutputID []byte    `json:"output_id"`
	PutAt    time.Time `json:"put_at"`
	Size     int64     `json:"size"`
	Object   string    `json:"object"`
}
type indexEntry struct {
	Key     string `json:"key"`
	Size    int64  `json:"size"`
	Access  int64  `json:"access"`
	Deleted bool   `json:"deleted,omitempty"`
	// Retry order is process-local. It never changes the persisted last use.
	retryAt int64
}
type Cache struct {
	dir        string
	maxBytes   int64
	mu         sync.Mutex
	lock       *os.File
	clientLock *os.File
	pins       string
	closed     bool
	index      map[string]indexEntry
	offset     int64
	journalID  os.FileInfo
	// journalGen is the key of the journal's first row as last replayed from
	// byte zero (a compaction marker when compaction wrote the file). Inode
	// identity alone cannot detect a rewritten journal: inode numbers are
	// reused, and on a busy cache gaterunner-3 cycled the index through
	// three inodes (one repeated) inside 18 seconds. Content identity can.
	journalGen string
	total      int64
	eviction   evictionHeap
}

// Lock patience is a var so tests can shrink it. Startup gets the long
// deadline: the initial replay runs under the lock, and a new client that
// cannot start at all fails every request of its Go process.
var authorityLockTimeout = 15 * time.Second
var authorityStartupLockTimeout = 60 * time.Second

// compactionMinBytes is a var so tests can exercise compaction cheaply.
var compactionMinBytes int64 = 8 << 20

// evictionHeap has one node per live entry; touches update in O(log n).
// A pass examines at most 256 candidates, including pinned candidates. A busy
// client may keep the cache over its soft target until later requests or close.
type evictionHeap struct {
	rows      []indexEntry
	positions map[string]int
}

func (h evictionHeap) Len() int { return len(h.rows) }
func (h evictionHeap) Less(i, j int) bool {
	priority := func(r indexEntry) int64 {
		if r.retryAt > r.Access {
			return r.retryAt
		}
		return r.Access
	}
	return priority(h.rows[i]) < priority(h.rows[j])
}
func (h evictionHeap) Swap(i, j int) {
	h.rows[i], h.rows[j] = h.rows[j], h.rows[i]
	h.positions[h.rows[i].Key] = i
	h.positions[h.rows[j].Key] = j
}
func (h *evictionHeap) Push(x any) {
	r := x.(indexEntry)
	h.positions[r.Key] = len(h.rows)
	h.rows = append(h.rows, r)
}
func (h *evictionHeap) Pop() any {
	n := len(h.rows) - 1
	r := h.rows[n]
	h.rows = h.rows[:n]
	delete(h.positions, r.Key)
	return r
}
func (c *Cache) applyRow(row indexEntry) {
	if c.eviction.positions == nil {
		c.eviction.positions = map[string]int{}
	}
	if old, ok := c.index[row.Key]; ok && !strings.HasPrefix(old.Key, "action-") {
		c.total -= old.Size
	}
	if row.Deleted {
		delete(c.index, row.Key)
		if i, ok := c.eviction.positions[row.Key]; ok {
			heap.Remove(&c.eviction, i)
		}
		return
	}
	c.index[row.Key] = row
	if !strings.HasPrefix(row.Key, "action-") {
		c.total += row.Size
	}
	if i, ok := c.eviction.positions[row.Key]; ok {
		c.eviction.rows[i] = row
		heap.Fix(&c.eviction, i)
	} else {
		heap.Push(&c.eviction, row)
	}
}

func New(dir string, maxBytes int64) (*Cache, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for _, part := range []string{"objects", "actions", "clients"} {
		if err := os.MkdirAll(filepath.Join(dir, part), 0700); err != nil {
			return nil, err
		}
	}
	lock, err := os.OpenFile(filepath.Join(dir, "authority.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	c := &Cache{dir: dir, maxBytes: maxBytes, lock: lock, index: map[string]indexEntry{}}
	err = c.withLockWithin(authorityStartupLockTimeout, func() error {
		// Client locks are held for the complete protocol lifetime. A dead process
		// releases its OS lock; PID reuse cannot make an abandoned pin live again.
		entries, err := os.ReadDir(filepath.Join(dir, "clients"))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			p := filepath.Join(dir, "clients", entry.Name())
			f, e := os.OpenFile(filepath.Join(p, "lock"), os.O_RDWR, 0600)
			if e != nil {
				continue
			}
			if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil {
				e = os.RemoveAll(p)
				if e != nil {
					f.Close()
					return e
				}
			}
			f.Close()
		}
		c.pins, err = os.MkdirTemp(filepath.Join(dir, "clients"), "client-")
		if err != nil {
			return err
		}
		c.clientLock, err = os.OpenFile(filepath.Join(c.pins, "lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		if err = unix.Flock(int(c.clientLock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return err
		}
		return c.loadIndex()
	})
	if err != nil {
		if c.clientLock != nil {
			c.clientLock.Close()
		}
		lock.Close()
		return nil, err
	}
	return c, nil
}
func (c *Cache) withLock(fn func() error) error {
	return c.withLockWithin(authorityLockTimeout, fn)
}
func (c *Cache) withLockWithin(limit time.Duration, fn func() error) error {
	deadline := time.Now().Add(limit)
	for {
		err := unix.Flock(int(c.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cache authority lock timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer unix.Flock(int(c.lock.Fd()), unix.LOCK_UN)
	return fn()
}

func (c *Cache) resetJournal() {
	c.index = map[string]indexEntry{}
	c.eviction = evictionHeap{positions: map[string]int{}}
	c.total = 0
	c.offset = 0
	c.journalGen = ""
}

// peekJournalFirstKey returns the key of the journal's first row. It reads
// through ReadAt, so it does not disturb the caller's seek position.
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
	}
	var head struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(line, &head) != nil || head.Key == "" {
		return "", false
	}
	return head.Key, true
}

// Replay only journal bytes added since this client last held the authority.
// A rewrite (compaction) is detected by inode, size, and — decisively — the
// journal's first-row key: inode numbers are reused under churn, so a client
// whose recorded inode happens to match a later incarnation of the file
// would otherwise resume at a stale offset mid-row and fail requests with
// "invalid cache index: invalid character ..." against a healthy file.
func (c *Cache) loadIndex() error {
	f, err := os.OpenFile(filepath.Join(c.dir, "index.jsonl"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if c.journalID == nil || !os.SameFile(info, c.journalID) || info.Size() < c.offset {
		c.resetJournal()
	}
	if c.offset > 0 {
		if key, ok := peekJournalFirstKey(f); !ok || key != c.journalGen {
			c.resetJournal()
		}
	}
	c.journalID = info
	if _, err = f.Seek(c.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(f)
	for {
		line, e := r.ReadSlice('\n')
		if e == io.EOF { // A killed append leaves no trusted transaction. Drop only the incomplete tail.
			if len(line) > 0 {
				return f.Truncate(c.offset)
			}
			return nil
		}
		if e != nil {
			return e
		}
		if len(line) > 4096 {
			return fmt.Errorf("oversized cache index row")
		}
		var row indexEntry
		if err = json.Unmarshal(line, &row); err != nil {
			// A syntactically torn row means the tail past the last good
			// row cannot be trusted (killed append, or a reader that
			// resumed mid-journal despite the generation check). Drop
			// it and serve what replayed cleanly: a cache index must
			// never fail a build. Rows with valid syntax but invalid
			// identity still hard-fail below — that is the escape
			// guard, and it is not reachable from a torn read of a
			// flat row.
			if terr := f.Truncate(c.offset); terr != nil {
				return terr
			}
			if c.offset == 0 {
				c.journalGen = ""
			}
			return nil
		}
		key := strings.TrimPrefix(row.Key, "action-")
		decoded, decodeErr := hex.DecodeString(key)
		action := strings.HasPrefix(row.Key, "action-")
		if decodeErr != nil || key != strings.ToLower(key) || (!action && len(decoded) != 32) || (action && !validID(decoded)) || row.Size < 0 || row.Size > MaxObjectBytes || row.Access < 0 {
			return fmt.Errorf("invalid cache index identity or size")
		}
		if c.offset == 0 {
			// First row of a from-zero replay names this incarnation of
			// the journal. Compaction writes a dedicated marker here:
			// size 0 with access 0 is unreachable for organic rows,
			// whose access is always a UnixNano timestamp.
			c.journalGen = row.Key
			if row.Size == 0 && row.Access == 0 && !row.Deleted {
				c.offset += int64(len(line))
				continue
			}
		}
		c.applyRow(row)
		c.offset += int64(len(line))
	}
}
func (c *Cache) record(row indexEntry) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	f, err := os.OpenFile(filepath.Join(c.dir, "index.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return err
	}
	if c.offset == 0 {
		c.journalGen = row.Key
	}
	c.applyRow(row)
	c.offset += int64(len(raw))
	return nil
}

// newJournalMarker returns a fresh 64-hex journal generation key. The
// marker row carries size 0 and access 0, a combination organic rows
// never have (their access is a UnixNano timestamp), and it passes the
// journal identity validation, so older binaries sharing the cache treat
// it as a harmless zero-size entry instead of hard-failing.
func newJournalMarker() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		raw = sha256.Sum256([]byte(fmt.Sprintf("journal-%d-%d", time.Now().UnixNano(), os.Getpid())))
	}
	return hex.EncodeToString(raw[:])
}
func atomicWrite(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func (c *Cache) actionPath(id []byte) string {
	return filepath.Join(c.dir, "actions", hex.EncodeToString(id)+".json")
}
func (c *Cache) objectPath(key string) string {
	if strings.HasPrefix(key, "action-") {
		return filepath.Join(c.dir, "actions", strings.TrimPrefix(key, "action-")+".json")
	}
	return filepath.Join(c.dir, "objects", key)
}
func validID(id []byte) bool { return len(id) > 0 && len(id) <= 128 }
func (c *Cache) pin(key string) (string, error) {
	p := filepath.Join(c.pins, key)
	err := os.Link(c.objectPath(key), p)
	if os.IsExist(err) {
		err = nil
	}
	return p, err
}
func (c *Cache) get(req Request) Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	resp := Response{ID: req.ID, Miss: true}
	if c.closed || !validID(req.ActionID) {
		return resp
	}
	err := c.withLock(func() error {
		if err := c.loadIndex(); err != nil {
			return err
		}
		raw, err := os.ReadFile(c.actionPath(req.ActionID))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var meta objectMeta
		if json.Unmarshal(raw, &meta) != nil || len(meta.Object) != 64 || strings.ContainsAny(meta.Object, "/\\") {
			return nil
		}
		info, err := os.Stat(c.objectPath(meta.Object))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Size() != meta.Size {
			return nil
		}
		path, err := c.pin(meta.Object)
		if err != nil {
			return err
		}
		if err = c.record(indexEntry{Key: meta.Object, Size: meta.Size, Access: time.Now().UnixNano()}); err != nil {
			return err
		}
		resp = Response{ID: req.ID, OutputID: meta.OutputID, Size: meta.Size, DiskPath: path, Time: &meta.PutAt}
		return nil
	})
	if err != nil {
		// A get must never fail a build: lock contention or a journal
		// problem degrades to a miss, and the caller recompiles.
		return Response{ID: req.ID, Miss: true}
	}
	return resp
}
func (c *Cache) put(req Request, body []byte) Response {
	return c.putReader(req, bytes.NewReader(body))
}
func (c *Cache) putReader(req Request, body io.Reader) Response {
	resp := Response{ID: req.ID}
	fail := func(err error) Response { resp.Err = err.Error(); return resp }
	if !validID(req.ActionID) || !validID(req.OutputID) || req.BodySize < 0 || req.BodySize > MaxObjectBytes {
		return fail(fmt.Errorf("invalid cache key or body size"))
	}
	f, err := os.CreateTemp(c.pins, ".object-")
	if err != nil {
		return fail(err)
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			os.Remove(f.Name())
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(body, req.BodySize+1))
	if err == nil && n != req.BodySize {
		err = fmt.Errorf("body size mismatch: expected %d, received %d", req.BodySize, n)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return fail(err)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	key := hex.EncodeToString(hash.Sum(nil))
	meta := objectMeta{OutputID: req.OutputID, PutAt: time.Now().UTC(), Size: n, Object: key}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fail(fmt.Errorf("cache is closed"))
	}
	err = c.withLock(func() error {
		if err := c.loadIndex(); err != nil {
			return err
		}
		// Register before publish: a crash can conservatively overcount a missing
		// object, but never leave an unaccounted published object.
		if err := c.record(indexEntry{Key: key, Size: n, Access: time.Now().UnixNano()}); err != nil {
			return err
		}
		if err := os.Link(f.Name(), c.objectPath(key)); err != nil && !os.IsExist(err) {
			return err
		}
		path, err := c.pin(key)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		if err = c.record(indexEntry{Key: "action-" + hex.EncodeToString(req.ActionID), Size: int64(len(raw)), Access: time.Now().UnixNano()}); err != nil {
			return err
		}
		if err = atomicWrite(c.actionPath(req.ActionID), raw); err != nil {
			return err
		}
		if err = c.evictLocked(); err != nil {
			return err
		}
		resp = Response{ID: req.ID, DiskPath: path, Size: n, OutputID: req.OutputID, Time: &meta.PutAt}
		return nil
	})
	if err != nil {
		// The object body is already complete and fsynced in this
		// client's pin directory. If the shared journal could not be
		// updated (lock contention, journal fault), serve the object
		// unindexed instead of failing the build: this caller gets
		// its file, later lookups simply miss. The pin directory is
		// removed at Close, which bounds the unindexed file's life.
		keepTemp = true
		return Response{ID: req.ID, DiskPath: f.Name(), Size: n, OutputID: req.OutputID, Time: &meta.PutAt}
	}
	return resp
}
func (c *Cache) maxEntries() int {
	n := c.maxBytes / 4096
	if n < 256 {
		n = 256
	}
	if n > 1000000 {
		n = 1000000
	}
	return int(n)
}
func (c *Cache) evictLocked() error {
	if c.total <= c.maxBytes && len(c.index) <= c.maxEntries() {
		return nil
	}
	var skipped []indexEntry
	defer func() {
		for _, row := range skipped {
			heap.Push(&c.eviction, row)
		}
	}()
	for scanned := 0; scanned < 256 && c.eviction.Len() > 0; scanned++ {
		row := heap.Pop(&c.eviction).(indexEntry)
		if c.total <= c.maxBytes && len(c.index) <= c.maxEntries() {
			skipped = append(skipped, row)
			break
		}
		if strings.HasPrefix(row.Key, "action-") && len(c.index) <= c.maxEntries() {
			row.retryAt = time.Now().Add(time.Second).UnixNano()
			skipped = append(skipped, row)
			continue
		}
		path := c.objectPath(row.Key)
		info, err := os.Stat(path)
		if err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
				row.retryAt = time.Now().Add(time.Second).UnixNano()
				skipped = append(skipped, row)
				continue
			}
			if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
				skipped = append(skipped, row)
				return err
			}
		} else if !os.IsNotExist(err) {
			skipped = append(skipped, row)
			return err
		}
		if err = c.record(indexEntry{Key: row.Key, Deleted: true}); err != nil {
			skipped = append(skipped, row)
			return err
		}
	}
	return nil
}
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	err := c.withLock(func() error {
		if err := c.loadIndex(); err != nil {
			return err
		}
		if err := os.RemoveAll(c.pins); err != nil {
			return err
		}
		if err := c.evictLocked(); err != nil {
			return err
		}
		// Bound replay costs, but compact rarely: this rewrite runs under
		// the authority lock, and the previous rule (any close past 8 MiB)
		// fired on essentially every client close of a busy cache —
		// gaterunner-3's journal was being rewritten several times a
		// minute, starving the lock and churning inodes. Rewrite only
		// when the compacted form at least halves the journal. The new
		// file opens with a fresh generation marker row; clients detect
		// the rewrite by that key, not by inode alone.
		if c.offset > compactionMinBytes {
			var b bytes.Buffer
			enc := json.NewEncoder(&b)
			marker := indexEntry{Key: newJournalMarker()}
			if err := enc.Encode(marker); err != nil {
				return err
			}
			for _, row := range c.index {
				if err := enc.Encode(row); err != nil {
					return err
				}
			}
			if int64(b.Len())*2 >= c.offset {
				return nil
			}
			path := filepath.Join(c.dir, "index.jsonl")
			if err := atomicWrite(path, b.Bytes()); err != nil {
				return err
			}
			c.offset = int64(b.Len())
			c.journalGen = marker.Key
			if nf, err := os.Open(path); err == nil {
				if st, err := nf.Stat(); err == nil {
					c.journalID = st
				}
				nf.Close()
			}
			return nil
		}
		return nil
	})
	if c.clientLock != nil {
		c.clientLock.Close()
	}
	c.lock.Close()
	return err
}
func Serve(in io.Reader, out io.Writer, dir string, maxBytes int64) (serveErr error) {
	c, err := New(dir, maxBytes)
	if err != nil {
		return err
	}
	defer func() { serveErr = errors.Join(serveErr, c.Close()) }()
	enc := json.NewEncoder(out)
	if err = enc.Encode(Response{KnownCommands: []string{"get", "put", "close"}}); err != nil {
		return err
	}
	r := bufio.NewReaderSize(in, maxRequestBytes)
	for {
		line, err := r.ReadSlice('\n')
		if err == io.EOF && len(line) == 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read bounded request: %w", err)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req Request
		if err = json.Unmarshal(line, &req); err != nil {
			return err
		}
		var resp Response
		switch req.Command {
		case "get":
			resp = c.get(req)
		case "put":
			if req.BodySize < 0 || req.BodySize > MaxObjectBytes {
				return fmt.Errorf("invalid body size")
			}
			if req.BodySize == 0 {
				resp = c.put(req, nil)
			} else {
				// Go emits an unescaped base64 JSON string. Decode directly into the
				// temporary object; no body-sized Go allocation is required.
				b, err := readNonspace(r)
				if err != nil || b != '"' {
					return fmt.Errorf("missing base64 body quote")
				}
				encodedLen := base64.StdEncoding.EncodedLen(int(req.BodySize))
				limited := &io.LimitedReader{R: r, N: int64(encodedLen)}
				resp = c.putReader(req, base64.NewDecoder(base64.StdEncoding, limited))
				if limited.N != 0 {
					return fmt.Errorf("incomplete base64 body")
				}
				b, err = r.ReadByte()
				if err != nil || b != '"' {
					return fmt.Errorf("invalid base64 body terminator")
				}
				b, err = r.ReadByte()
				if err != nil || b != '\n' {
					return fmt.Errorf("invalid body newline")
				}
			}
		case "close":
			err = c.Close()
			resp = Response{ID: req.ID}
			if err != nil {
				resp.Err = err.Error()
			}
		default:
			resp = Response{ID: req.ID, Err: "unsupported command"}
		}
		if err = enc.Encode(resp); err != nil {
			return err
		}
		if req.Command == "close" {
			return nil
		}
	}
}

func readNonspace(r *bufio.Reader) (byte, error) {
	for i := 0; i < 64; i++ {
		b, e := r.ReadByte()
		if e != nil {
			return b, e
		}
		if b != ' ' && b != '\n' && b != '\r' && b != '\t' {
			return b, nil
		}
	}
	return 0, fmt.Errorf("excessive protocol whitespace")
}
