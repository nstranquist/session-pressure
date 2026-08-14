//go:build !darwin && !linux

package sessionpressure

import "fmt"

func processStartIdentity(pid int) (string, error) {
	return "", fmt.Errorf("process start identity is unavailable for PID %d on this platform", pid)
}
