// Package filelock provides advisory file locking with cross-process atomic
// acquisition and an in-process FIFO queue.
//
// Ported from veritas-kanban's server/src/services/file-lock.ts (MIT) — see
// the dissection at ~/code/refs/_cases/veritas-kanban/
// for the original walkthrough.
//
// Design:
//
//   - Cross-process safety: ownership JSON is written and fsynced privately,
//     then a same-directory hard link atomically publishes path + ".lock".
//     Readers therefore never observe a half-initialized new sidecar.
//   - In-process safety: a per-key channel queue serializes goroutines in the
//     same process before they race the OS lock. Without this, two goroutines
//     hitting O_EXCL nearly simultaneously can both lose ordering even though
//     only one wins the file.
//   - Stale recovery: lock file carries {pid, timestamp}. Missing, malformed,
//     or dead-PID locks are reclaimed. A live owner is never preempted solely
//     because a critical section outlived StaleAge; waiters time out safely.
//
// Sibling to the existing `internal/lock/` package, which is the `ndev lock`
// CLI dispatcher (different audience, different concern).
package filelock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Defaults match the upstream TS implementation.
const (
	DefaultTimeout = 5 * time.Second
	StaleAge       = 30 * time.Second
	pollInterval   = 50 * time.Millisecond
	// A newly visible sidecar written by an n-1 process can briefly be empty
	// between O_EXCL creation and its first write. Do not steal malformed state
	// during that compatibility window. New writers avoid the window entirely
	// by publishing a fully initialized sidecar with an atomic hard link.
	lockInitializationGrace = StaleAge
)

// info is the JSON payload written to the sidecar lock file.
type info struct {
	PID       int    `json:"pid"`
	Timestamp int64  `json:"timestamp"` // unix millis
	Token     string `json:"token,omitempty"`
}

// queues serializes in-process callers per resolved path.
//
// The value is a channel that the holder closes (well, sends-then-closes) to
// release. Each waiter parks on the previous tail and installs itself as the
// new tail before being granted.
var (
	queuesMu sync.Mutex
	queues   = make(map[string]chan struct{})
)

// enqueue waits for the in-process turn for key. The returned release MUST be
// called even on error so the next waiter doesn't stall.
//
// timeout bounds the wait. If we time out, we still chain a release onto the
// previous holder so the queue keeps moving once they're done.
func enqueue(ctx context.Context, key string, timeout time.Duration) (release func(), err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	queuesMu.Lock()
	prev := queues[key]
	mine := make(chan struct{})
	queues[key] = mine
	queuesMu.Unlock()

	released := false
	var releaseMu sync.Mutex
	releaseFn := func() {
		releaseMu.Lock()
		defer releaseMu.Unlock()
		if released {
			return
		}
		released = true
		// Pop ourselves out of the map only if we're still the tail.
		queuesMu.Lock()
		if queues[key] == mine {
			delete(queues, key)
		}
		queuesMu.Unlock()
		close(mine)
	}

	if prev == nil {
		return releaseFn, nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-prev:
		return releaseFn, nil
	case <-ctx.Done():
		go func() {
			<-prev
			releaseFn()
		}()
		return func() {}, fmt.Errorf("filelock: ctx canceled while queued for %s: %w", key, ctx.Err())
	case <-timer.C:
		// Don't stall the queue. Hand off our slot to whatever the previous
		// holder finishes — when they release, we release too, unblocking the
		// next waiter.
		go func() {
			<-prev
			releaseFn()
		}()
		return func() {}, fmt.Errorf("filelock: in-process queue timeout after %s", timeout)
	}
}

// isProcessAlive returns true if PID still exists. On Unix, signal 0 is the
// canonical liveness probe.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		// errors.Is(err, syscall.ESRCH) is the "no such process" case; permission
		// errors (EPERM) mean the process exists but we can't signal it — still alive.
		return errors.Is(err, syscall.EPERM)
	}
	return true
}

// readLockInfo returns the info struct or nil if missing/malformed.
func readLockInfo(lockFile string) *info {
	data, err := os.ReadFile(lockFile)
	if err != nil {
		return nil
	}
	var i info
	if err := json.Unmarshal(data, &i); err != nil {
		return nil
	}
	if i.PID <= 0 || i.Timestamp <= 0 {
		return nil
	}
	return &i
}

// isStale returns true if the lock file is missing, old-and-malformed, or owned
// by a dead PID. Timestamp age is diagnostic only while the PID is alive:
// stealing an aged live lock would allow overlapping critical sections.
func isStale(lockFile string) bool {
	i := readLockInfo(lockFile)
	if i == nil {
		stat, err := os.Stat(lockFile)
		if err == nil && time.Since(stat.ModTime()) < lockInitializationGrace {
			return false
		}
		return true
	}
	return !isProcessAlive(i.PID)
}

// tryCreateLock builds the ownership record privately, fsyncs it, then uses a
// same-directory hard link as the O_EXCL publication point. A competing reader
// can therefore see either no sidecar or a complete sidecar, never the empty
// initialization window that allowed two live processes to enter together.
func tryCreateLock(lockFile string) (*info, error) {
	if err := os.MkdirAll(filepath.Dir(lockFile), 0o755); err != nil {
		return nil, err
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, fmt.Errorf("generate ownership token: %w", err)
	}
	owner := &info{
		PID:       os.Getpid(),
		Timestamp: time.Now().UnixMilli(),
		Token:     hex.EncodeToString(tokenBytes[:]),
	}
	payload, err := json.Marshal(owner)
	if err != nil {
		return nil, fmt.Errorf("encode ownership token: %w", err)
	}
	tmp := fmt.Sprintf("%s.tmp-%d-%s", lockFile, owner.PID, owner.Token)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		_ = os.Remove(tmp)
	}()
	if _, err := f.Write(payload); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	closed = true
	if err := os.Link(tmp, lockFile); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("publish initialized lock sidecar: %w", err)
	}
	return owner, nil
}

// removeLock deletes the lock file. Missing-file is not an error.
func removeLock(lockFile string) {
	if err := os.Remove(lockFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Soft-log via the error channel of any caller that cares; we keep this
		// silent because the unlock contract says "best effort."
		_ = err
	}
}

// removeOwnedLock deletes only the caller's acquisition. This makes unlock
// idempotent and prevents a delayed unlock from deleting a successor lock if
// the sidecar was externally replaced.
func removeOwnedLock(lockFile string, owner *info) {
	if owner == nil || owner.Token == "" {
		return
	}
	current := readLockInfo(lockFile)
	if current == nil || current.Token != owner.Token {
		return
	}
	removeLock(lockFile)
}

// Acquire takes an advisory file lock on path. Returns an Unlock function that
// MUST be called when the critical section ends. If timeout is zero,
// DefaultTimeout is used.
//
// The lock file lives at path+".lock". The target file itself does not need to
// exist.
func Acquire(path string, timeout time.Duration) (unlock func(), err error) {
	return AcquireContext(context.Background(), path, timeout)
}

// AcquireContext is like Acquire but honors ctx cancellation in addition to
// the timeout.
func AcquireContext(ctx context.Context, path string, timeout time.Duration) (unlock func(), err error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("filelock: abs(%q): %w", path, err)
	}
	lockFile := abs + ".lock"
	deadline := time.Now().Add(timeout)

	queueRel, qerr := enqueue(ctx, abs, timeout)
	if qerr != nil {
		return nil, qerr
	}

	released := false
	releaseQueue := func() {
		if released {
			return
		}
		released = true
		queueRel()
	}

	for {
		if ctx.Err() != nil {
			releaseQueue()
			return nil, fmt.Errorf("filelock: ctx canceled while waiting for %s: %w", path, ctx.Err())
		}
		owner, cerr := tryCreateLock(lockFile)
		if cerr != nil {
			releaseQueue()
			return nil, fmt.Errorf("filelock: create lock %s: %w", lockFile, cerr)
		}
		if owner != nil {
			var once sync.Once
			return func() {
				once.Do(func() {
					removeOwnedLock(lockFile, owner)
					releaseQueue()
				})
			}, nil
		}
		if isStale(lockFile) {
			removeLock(lockFile)
			continue
		}
		if time.Now().After(deadline) {
			releaseQueue()
			return nil, fmt.Errorf("filelock: failed to acquire %s within %s", path, timeout)
		}
		select {
		case <-ctx.Done():
			releaseQueue()
			return nil, fmt.Errorf("filelock: ctx canceled while waiting for %s: %w", path, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// WithFileLock holds the lock for the duration of fn. Any error from fn is
// returned unchanged; lock acquisition errors are returned as-is.
func WithFileLock(path string, fn func() error) error {
	return WithFileLockContext(context.Background(), path, DefaultTimeout, fn)
}

// WithFileLockContext is the context-aware variant of WithFileLock.
func WithFileLockContext(ctx context.Context, path string, timeout time.Duration, fn func() error) error {
	unlock, err := AcquireContext(ctx, path, timeout)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}
