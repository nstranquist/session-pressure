//go:build !darwin && !linux

package sessionpressure

import "fmt"

func processGuardIdentity(int) (string, int, error) {
	return "", 0, fmt.Errorf("process incidents require kernel identity support")
}
