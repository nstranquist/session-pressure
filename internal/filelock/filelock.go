// Package filelock provides advisory file locking with cross-process atomic
// acquisition and an in-process FIFO queue.
//
// Ported from veritas-kanban's server/src/services/file-lock.ts (MIT) — see
// the dissection at ~/code/refs/_cases/veritas-kanban/
// for the original walkthrough.
//
// Design:
//
//   - Cross-process safety: ownership JSON is written and closed privately,
//     then one atomic call publishes path + ".lock": an exclusive rename
//     (renameat2 RENAME_NOREPLACE, renamex_np RENAME_EXCL) where the file
//     system supports it, else a same-directory hard link.
//     Readers therefore never observe a half-initialized new sidecar.
//   - In-process safety: a per-key channel queue serializes goroutines in the
//     same process before they race the OS lock. Without this, two goroutines
//     hitting O_EXCL nearly simultaneously can both lose ordering even though
//     only one wins the file.
//   - Stale recovery: lock file carries {pid, timestamp, token}. A missing
//     sidecar is never removed: the waiter retries the create at once. A
//     dead-PID sidecar, or a malformed one older than the n-1 initialization
//     grace, is removed only by the one waiter that holds the kernel reclaim
//     guard (flock on path + ".lock.reclaim") and that judges it stale again
//     under the guard. Without the guard, a waiter that judged an old sidecar
//     stale could delete a successor's live sidecar and let two processes in.
//     A live owner is never preempted solely because a critical
//     section outlived StaleAge; waiters time out safely. A dead owner's PID
//     reused by a process that started after the sidecar was written is not
//     the owner, so that sidecar is stale too. Temp ownership
//     records of dead processes are swept on the same guarded stale path.
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// ownerStartSlack absorbs clock error between a process's recorded start
	// time and the wall-clock timestamp it later writes into its sidecar:
	// /proc/stat btime has one-second resolution, and the wall clock can step
	// backwards. A smaller slack risks judging a real owner reused, which
	// would let two processes in; a larger one only delays recovery when a PID
	// is reused within the slack of the dead owner's acquisition.
	ownerStartSlack = 10 * time.Second
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
		return func() {}, &timeoutError{path: key, timeout: timeout, queued: true}
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

// sidecarState is a waiter's judgement of the current lock sidecar.
type sidecarState int

const (
	sidecarLive sidecarState = iota
	sidecarMissing
	sidecarStale
)

// judgeSidecar classifies the lock file. A malformed sidecar is stale only
// after the n-1 initialization grace. Timestamp age is diagnostic only while
// the owner is alive: stealing an aged live lock would allow overlapping
// critical sections. The owner is the recorded PID only if that process
// started before the sidecar was written.
func judgeSidecar(lockFile string) sidecarState {
	if i := readLockInfo(lockFile); i != nil {
		if ownerAlive(i.PID, time.UnixMilli(i.Timestamp)) {
			return sidecarLive
		}
		return sidecarStale
	}
	stat, err := os.Stat(lockFile)
	if errors.Is(err, os.ErrNotExist) {
		return sidecarMissing
	}
	if err == nil && time.Since(stat.ModTime()) < lockInitializationGrace {
		return sidecarLive
	}
	return sidecarStale
}

// processInfo is what the kernel says about a PID's process.
type processInfo struct {
	started time.Time
	// zombie means the process exited and waits for its parent to reap it.
	// kill(pid, 0) still succeeds for it.
	zombie bool
}

// readProcessInfo reports on pid. Tests replace it to pin the judgement on
// hosts where real process ages cannot be arranged.
var readProcessInfo = platformProcessInfo

// ownerAlive reports whether the process that wrote a record at recordedAt
// under pid still runs. A dead PID is not the owner, and neither is a zombie:
// it exited inside the lock and only waits for its parent. A live PID whose
// process started more than ownerStartSlack after recordedAt is a different
// process that reused the PID after the owner died (killed inside the lock,
// or a host restart), so it is not the owner either. When the kernel cannot
// be read, a live PID counts as the owner: the safe side, never two inside.
func ownerAlive(pid int, recordedAt time.Time) bool {
	if !isProcessAlive(pid) {
		return false
	}
	proc, ok := readProcessInfo(pid)
	if !ok {
		return true
	}
	if proc.zombie {
		return false
	}
	return !proc.started.After(recordedAt.Add(ownerStartSlack))
}

// sweepOwnerlessTemps removes ownership records that a crashed process left
// between creating its private temp file and publishing it (or between a
// hard-link publish and the temp unlink). It runs only on the stale path,
// under the reclaim guard. A temp file of a live owner is never removed: its
// publish would fail. The file's own modification time stands in for the
// record time.
func sweepOwnerlessTemps(lockFile string) {
	dir, base := filepath.Split(lockFile)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := base + ".tmp-"
	for _, entry := range entries {
		name := entry.Name()
		rest, found := strings.CutPrefix(name, prefix)
		if !found {
			continue
		}
		pidText, _, found := strings.Cut(rest, "-")
		pid, err := strconv.Atoi(pidText)
		if !found || err != nil || pid <= 0 {
			continue
		}
		stat, err := entry.Info()
		if err != nil || !stat.Mode().IsRegular() || ownerAlive(pid, stat.ModTime()) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// testHookBeforeReclaimGuard runs after a waiter first judges the sidecar
// stale and before it takes the reclaim guard. Tests use it to put a
// competing waiter into that window.
var testHookBeforeReclaimGuard func()

// testHookAfterReclaimLocks runs after a waiter holds the inode lock and the
// guard and before it checks that the path still names the locked inode.
var testHookAfterReclaimLocks func()

// reclaimStale removes a stale sidecar and reports whether the caller should
// try the create again at once.
//
// A missing sidecar is not removed: the owner released it, so a retry either
// wins or finds the successor. A stale sidecar is removed only while this
// waiter holds both the judged file's inode lock and the non-blocking reclaim
// guard, after it confirms the path still names that inode and judges it
// again. Inside the guard no
// other waiter removes the sidecar and os.Link never replaces an existing
// one, so the file judged stale is the file removed. The guard is
// non-blocking, so a stopped reclaimer makes others poll, not hang, and the
// kernel drops it when its process exits.
func reclaimStale(lockFile string) bool {
	switch judgeSidecar(lockFile) {
	case sidecarMissing:
		return true
	case sidecarLive:
		return false
	}
	if hook := testHookBeforeReclaimGuard; hook != nil {
		hook()
	}
	// First lock the judged file's own inode. nicos-tools breakers
	// (filelock.BreakStale) hold this lock, not the guard file, so taking both
	// makes this reclaim exclude theirs on files both repositories write, such
	// as the notify inbox.
	judged, err := os.OpenFile(lockFile, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	defer judged.Close()
	if err := syscall.Flock(int(judged.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	defer syscall.Flock(int(judged.Fd()), syscall.LOCK_UN)
	// Then the guard file, which n-1 binaries of this repository use.
	guard, err := os.OpenFile(lockFile+".reclaim", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer guard.Close()
	fd := int(guard.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Another waiter is reclaiming; poll and judge again later.
		return false
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	if hook := testHookAfterReclaimLocks; hook != nil {
		hook()
	}
	// The path must still name the inode this waiter locked: otherwise
	// another breaker removed it and a successor may have published.
	opened, openedErr := judged.Stat()
	current, currentErr := os.Lstat(lockFile)
	if errors.Is(currentErr, os.ErrNotExist) {
		return true
	}
	if openedErr != nil || currentErr != nil || !os.SameFile(opened, current) {
		return false
	}
	switch judgeSidecar(lockFile) {
	case sidecarMissing:
		return true
	case sidecarLive:
		return false
	}
	sweepOwnerlessTemps(lockFile)
	if err := os.Remove(lockFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		// An undeletable sidecar must not spin the caller; poll until timeout.
		return false
	}
	return true
}

// exclusiveRename publishes the finished sidecar in one call when the
// platform and file system support it. Tests and benchmarks set it to nil
// to force the hard-link path.
var exclusiveRename = platformExclusiveRename

// exclusiveRenameUnsupported records that the file system refused the
// exclusive rename, so later attempts go straight to the hard link.
var exclusiveRenameUnsupported atomic.Bool

// tryCreateLock builds and closes the ownership record privately, then
// publishes it atomically: an exclusive rename where supported, else a
// same-directory hard link. Both fail when the sidecar exists, so they are
// the O_EXCL point. A competing reader can therefore see either no sidecar or
// a complete sidecar, never the empty initialization window that allowed two
// live processes to enter together.
func tryCreateLock(lockFile string) (*info, error) {
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
	if errors.Is(err, os.ErrNotExist) {
		// Create the directory only when it is missing, not on every attempt.
		if err := os.MkdirAll(filepath.Dir(lockFile), 0o755); err != nil {
			return nil, err
		}
		f, err = os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	if err != nil {
		return nil, err
	}
	closed, tmpGone := false, false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		if !tmpGone {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(payload); err != nil {
		return nil, err
	}
	// This record synchronizes live processes; it is not durable application
	// state. Write and close make the complete payload visible before the
	// publish. Sync would force an extra disk flush for every lock attempt,
	// including losing contenders. After a host restart the recorded PID is
	// either gone or names a process that started after the record, and
	// judgeSidecar treats both as stale. Data durability belongs to the
	// protected writer, whose syncs remain unchanged.
	if err := f.Close(); err != nil {
		return nil, err
	}
	closed = true
	if rename := exclusiveRename; rename != nil && !exclusiveRenameUnsupported.Load() {
		err := rename(tmp, lockFile)
		switch {
		case err == nil:
			tmpGone = true
			return owner, nil
		case errors.Is(err, os.ErrExist):
			return nil, nil
		case errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.ENOSYS),
			errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EOPNOTSUPP):
			// The file system has no exclusive rename; use the hard link.
			exclusiveRenameUnsupported.Store(true)
		default:
			return nil, fmt.Errorf("publish initialized lock sidecar: %w", err)
		}
	}
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

// ErrTimeout matches a lock wait that exhausted its own timeout while the
// caller's context was still live. Callers with a longer wait budget treat it
// as transient contention (for example a holder paged out during swap thrash)
// rather than as a terminal failure.
var ErrTimeout = errors.New("filelock: acquire timed out")

type timeoutError struct {
	path    string
	timeout time.Duration
	// queued marks expiry in the in-process FIFO before the file was tried.
	queued bool
}

func (err *timeoutError) Error() string {
	if err.queued {
		return fmt.Sprintf("filelock: in-process queue timeout after %s", err.timeout)
	}
	return fmt.Sprintf("filelock: failed to acquire %s within %s", err.path, err.timeout)
}

func (err *timeoutError) Is(target error) bool { return target == ErrTimeout }

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
	// Most writers hold the lock for a few milliseconds. Start with short
	// sleeps so a brief collision does not add a fixed 50ms to the command.
	// Back off to the existing cadence for long-running holders.
	retryInterval := time.Millisecond

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
		if reclaimStale(lockFile) {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			releaseQueue()
			return nil, &timeoutError{path: path, timeout: timeout}
		}
		select {
		case <-ctx.Done():
			releaseQueue()
			return nil, fmt.Errorf("filelock: ctx canceled while waiting for %s: %w", path, ctx.Err())
		case <-time.After(min(retryInterval, remaining)):
		}
		retryInterval = min(retryInterval*2, pollInterval)
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
