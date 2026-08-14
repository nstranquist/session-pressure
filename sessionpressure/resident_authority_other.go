//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package sessionpressure

import (
	"path/filepath"
	"time"

	"github.com/nstranquist/session-pressure/internal/filelock"
)

func AcquireResidentAuthority(dir string) (func(), error) {
	return filelock.Acquire(filepath.Join(dir, "resident-authority"), time.Second)
}
