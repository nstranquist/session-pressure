//go:build !darwin

package sessionpressure

import (
	"context"
	"fmt"
)

func nativeDiskDeviceCounter(context.Context) (diskDeviceCounter, error) {
	return diskDeviceCounter{}, fmt.Errorf("internal SSD write counters are unavailable on this platform")
}

func nativeDiskProcessCounters(context.Context) (diskProcessSnapshot, error) {
	return diskProcessSnapshot{}, fmt.Errorf("per-process disk-write counters are unavailable on this platform")
}
