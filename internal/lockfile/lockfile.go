// Package lockfile provides small cross-process advisory lock files.
package lockfile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultTimeout = 5 * time.Second
	pollInterval   = 50 * time.Millisecond
	staleMaxAge    = 30 * time.Second
)

// ErrLocked is returned when a lock cannot be acquired before the timeout.
var ErrLocked = errors.New("lockfile: lock held by another process")

var localQueues sync.Map

type lockInfo struct {
	PID       int    `json:"pid"`
	Timestamp int64  `json:"timestamp"`
	Token     string `json:"token,omitempty"`
}

// Lock acquires an advisory lock for path and returns an idempotent unlock
// function. The lock state is stored in path+".lock".
func Lock(path string) (func(), error) {
	return LockTimeout(path, defaultTimeout)
}

// LockTimeout is Lock with a caller-owned contention budget. It is intended
// for best-effort observability paths that must never inherit the five-second
// control-plane default.
func LockTimeout(path string, timeout time.Duration) (func(), error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("lockfile: empty path")
	}
	if timeout < 0 {
		return nil, errors.New("lockfile: timeout must be non-negative")
	}

	key, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("lockfile: resolve path: %w", err)
	}
	mu := localMutex(key)
	mu.Lock()

	token, err := newToken()
	if err != nil {
		mu.Unlock()
		return nil, err
	}

	lockPath := path + ".lock"
	deadline := time.Now().Add(timeout)
	for {
		acquired, err := tryCreateLock(lockPath, token)
		if err != nil {
			mu.Unlock()
			return nil, err
		}
		if acquired {
			var once sync.Once
			return func() {
				once.Do(func() {
					removeOwnedLock(lockPath, token)
					mu.Unlock()
				})
			}, nil
		}

		stale, err := isLockStale(lockPath, time.Now())
		if err != nil {
			mu.Unlock()
			return nil, err
		}
		if stale {
			_ = os.Remove(lockPath)
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			mu.Unlock()
			return nil, fmt.Errorf("lockfile: failed to acquire %s within %s: %w", path, timeout, ErrLocked)
		}
		if remaining > pollInterval {
			remaining = pollInterval
		}
		time.Sleep(remaining)
	}
}

// WithFileLock runs fn while holding Lock(path).
func WithFileLock(path string, fn func() error) error {
	return WithFileLockTimeout(path, defaultTimeout, fn)
}

// WithFileLockTimeout runs fn under a lock with a caller-owned wait budget.
func WithFileLockTimeout(path string, timeout time.Duration, fn func() error) error {
	if fn == nil {
		return errors.New("lockfile: nil function")
	}
	unlock, err := LockTimeout(path, timeout)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func localMutex(key string) *sync.Mutex {
	mu, _ := localQueues.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func tryCreateLock(lockPath, token string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return false, fmt.Errorf("lockfile: create lock dir: %w", err)
	}

	info := lockInfo{
		PID:       os.Getpid(),
		Timestamp: time.Now().UnixMilli(),
		Token:     token,
	}
	body, err := json.Marshal(info)
	if err != nil {
		return false, fmt.Errorf("lockfile: marshal lock info: %w", err)
	}
	body = append(body, '\n')

	f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lockfile: create lock file: %w", err)
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(lockPath)
	}

	n, err := f.Write(body)
	if err != nil {
		cleanup()
		return false, fmt.Errorf("lockfile: write lock file: %w", err)
	}
	if n != len(body) {
		cleanup()
		return false, fmt.Errorf("lockfile: write lock file: short write")
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(lockPath)
		return false, fmt.Errorf("lockfile: close lock file: %w", err)
	}
	return true, nil
}

func readLockInfo(lockPath string) (*lockInfo, error) {
	body, err := os.ReadFile(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lockfile: read lock file: %w", err)
	}
	var info lockInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, nil
	}
	if info.PID <= 0 || info.Timestamp <= 0 {
		return nil, nil
	}
	return &info, nil
}

func isLockStale(lockPath string, now time.Time) (bool, error) {
	info, err := readLockInfo(lockPath)
	if err != nil {
		return false, err
	}
	if info == nil {
		return true, nil
	}
	if !isProcessAlive(info.PID) {
		return true, nil
	}
	createdAt := time.UnixMilli(info.Timestamp)
	return now.Sub(createdAt) > staleMaxAge, nil
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func removeOwnedLock(lockPath, token string) {
	info, err := readLockInfo(lockPath)
	if err != nil || info == nil {
		return
	}
	if info.Token != "" && info.Token != token {
		return
	}
	_ = os.Remove(lockPath)
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("lockfile: generate token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
