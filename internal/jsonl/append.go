package jsonl

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nstranquist/session-pressure/internal/lockfile"
)

// AppendLine appends body plus one trailing newline to path while holding the
// file's lockfile. body should be one already-marshaled JSON value.
func AppendLine(path string, body []byte, perm os.FileMode) error {
	return appendLine(path, body, perm, false, 0)
}

// AppendLineWithin is AppendLine with a bounded lock wait. A zero timeout uses
// the lockfile package default; positive values cap contention latency.
func AppendLineWithin(path string, body []byte, perm os.FileMode, timeout time.Duration) error {
	return appendLine(path, body, perm, false, timeout)
}

// AppendLineDurable is AppendLine with an fsync before close. Use it for
// operator-owned control-plane ledgers where acknowledging a write before it is
// on stable storage would make a later decision untrustworthy.
func AppendLineDurable(path string, body []byte, perm os.FileMode) error {
	return appendLine(path, body, perm, true, 0)
}

func appendLine(path string, body []byte, perm os.FileMode, durable bool, timeout time.Duration) error {
	if path == "" {
		return errors.New("jsonl: empty path")
	}
	line := append(append([]byte(nil), body...), '\n')

	withLock := lockfile.WithFileLock
	if timeout > 0 {
		withLock = func(path string, fn func() error) error {
			return lockfile.WithFileLockTimeout(path, timeout, fn)
		}
	}
	return withLock(path, func() error {
		dirPerm := os.FileMode(0o755)
		if perm.Perm()&0o077 == 0 {
			dirPerm = 0o700
		}
		if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
			return fmt.Errorf("jsonl: create log dir: %w", err)
		}
		if dirPerm == 0o700 {
			if err := os.Chmod(filepath.Dir(path), dirPerm); err != nil {
				return fmt.Errorf("jsonl: secure log dir: %w", err)
			}
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, perm)
		if err != nil {
			return fmt.Errorf("jsonl: open %s: %w", path, err)
		}
		if err := f.Chmod(perm); err != nil {
			closeErr := f.Close()
			return fmt.Errorf("jsonl: secure %s: %w", path, errors.Join(err, closeErr))
		}
		n, writeErr := f.Write(line)
		var syncErr error
		if writeErr == nil && n == len(line) && durable {
			syncErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			return fmt.Errorf("jsonl: append %s: %w", path, errors.Join(writeErr, closeErr))
		}
		if n != len(line) {
			return fmt.Errorf("jsonl: append %s: %w", path, errors.Join(io.ErrShortWrite, closeErr))
		}
		if syncErr != nil {
			return fmt.Errorf("jsonl: sync %s: %w", path, errors.Join(syncErr, closeErr))
		}
		if closeErr != nil {
			return fmt.Errorf("jsonl: close %s: %w", path, closeErr)
		}
		return nil
	})
}
