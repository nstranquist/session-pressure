//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package processtree

import "os/exec"

// Non-POSIX platforms retain os/exec's direct-child cancellation. WaitDelay
// still guarantees that inherited output descriptors cannot block forever.
func configurePlatform(_ *exec.Cmd) {}
