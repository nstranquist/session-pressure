// Package gocachelru implements GOCACHEPROG with immutable objects and a soft
// size target. Files returned to a Go process stay pinned until close or crash.
package gocachelru

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
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
type Cache struct {
	dir         string
	maxBytes    int64
	graceWindow time.Duration
	mu          sync.Mutex
	lock        *os.File
	clientLock  *os.File
	pins        string
	closed      bool
	// db is the SQLite index (index.go). Only a client that puts opens it:
	// a get and a get-only close never need it.
	db *sql.DB
	// indexed is set once this client has opened the index under the
	// authority lock; its close then runs an eviction pass.
	indexed bool
	// hints are this client's get access times, written in one
	// transaction at close.
	hints map[string]indexEntry
	// retry holds pinned eviction candidates this client demotes until the
	// given UnixNano time. It is process-local and never persisted.
	retry map[string]int64
}

// Lock patience is a var so tests can shrink it. Startup gets the long
// deadline: the initial replay runs under the lock, and a new client that
// cannot start at all fails every request of its Go process.
var authorityLockTimeout = 15 * time.Second
var authorityStartupLockTimeout = 60 * time.Second

// defaultEvictGraceWindow shields recently accessed objects across client Close
// boundaries so short-lived subprocesses (e.g. go/packages) still find DiskPath.
var defaultEvictGraceWindow = 5 * time.Minute

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
	c := &Cache{dir: dir, maxBytes: maxBytes, graceWindow: defaultEvictGraceWindow, lock: lock, retry: map[string]int64{}}
	// Startup does not open the index. Every go command starts a helper,
	// and a get needs no index; a client opens it on its first put.
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
		return unix.Flock(int(c.clientLock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
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
	// A get takes no authority lock. Every file it reads is published
	// atomically (rename or link), and eviction never removes an object
	// with a pin link, so a get that races an eviction either pins the
	// object first or sees it gone and misses. Under the lock, eight
	// clients on runner-3 waited 33-40 ms per get (2026-11-15).
	err := func() error {
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
		if _, err := c.pin(meta.Object); err != nil {
			return err
		}
		if c.hints == nil {
			c.hints = map[string]indexEntry{}
		}
		c.hints[meta.Object] = indexEntry{Key: meta.Object, Size: meta.Size, Access: time.Now().UnixNano()}
		resp = Response{ID: req.ID, OutputID: meta.OutputID, Size: meta.Size, DiskPath: c.objectPath(meta.Object), Time: &meta.PutAt}
		return nil
	}()
	if err != nil {
		// A get must never fail a build: a file problem degrades to a
		// miss, and the caller recompiles.
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
		if err := c.openIndexLocked(); err != nil {
			return err
		}
		c.indexed = true
		// Register before publish: a crash can conservatively overcount a
		// missing object, but never leave an unaccounted published object.
		now := time.Now().UnixNano()
		raw, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		action := indexEntry{Key: "action-" + hex.EncodeToString(req.ActionID), Size: int64(len(raw)), Access: now}
		if err := c.recordPut(indexEntry{Key: key, Size: n, Access: now}, action); err != nil {
			return err
		}
		if err := os.Link(f.Name(), c.objectPath(key)); err != nil && !os.IsExist(err) {
			return err
		}
		if _, err = c.pin(key); err != nil {
			return err
		}
		if err = atomicWrite(c.actionPath(req.ActionID), raw); err != nil {
			return err
		}
		if err = c.evictLocked(); err != nil {
			return err
		}
		resp = Response{ID: req.ID, DiskPath: c.objectPath(key), Size: n, OutputID: req.OutputID, Time: &meta.PutAt}
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
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	defer func() {
		if c.db != nil {
			c.db.Close()
		}
		if c.clientLock != nil {
			c.clientLock.Close()
		}
		c.lock.Close()
	}()
	if !c.indexed {
		// A get-only client added no bytes, so it owes no eviction: it
		// records its hints and drops its pins, both without the authority
		// lock. Hints go first, while the pins still hold the objects.
		c.flushHintsUnlocked()
		return os.RemoveAll(c.pins)
	}
	return c.withLock(func() error {
		if err := c.openIndexLocked(); err != nil {
			return err
		}
		if len(c.hints) > 0 {
			_ = writeHints(c.db, c.hints)
		}
		if err := os.RemoveAll(c.pins); err != nil {
			return err
		}
		return c.evictLocked()
	})
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
