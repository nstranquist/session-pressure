//go:build darwin || linux || freebsd || openbsd || netbsd

package sessionpressure

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// AcquireResidentAuthority holds a kernel-released process-lifetime lock. A
// stale file is harmless: flock ownership disappears automatically on crash,
// avoiding PID-reuse ambiguity in destructive-action authority.
func AcquireResidentAuthority(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "resident-authority.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open resident authority lock: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure resident authority lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errors.New("resident pressure authority is already held by another live process")
		}
		return nil, fmt.Errorf("lock resident pressure authority: %w", err)
	}
	if err := file.Truncate(0); err == nil {
		_, _ = file.Seek(0, 0)
		_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
			_ = file.Close()
		})
	}, nil
}
