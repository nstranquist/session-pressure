//go:build !darwin

package sessionpressure

import (
	"context"
	"fmt"
)

func nativePhysicalMemoryMB() (float64, error) {
	return 0, fmt.Errorf("native physical memory sampler unavailable")
}

func nativeSwapUsedMB() (float64, error) {
	return 0, fmt.Errorf("native swap sampler unavailable")
}

func nativeSwapIOCounter(context.Context) (swapIOCounter, error) {
	return swapIOCounter{}, fmt.Errorf("native swap I/O sampler unavailable")
}
