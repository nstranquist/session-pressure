//go:build !darwin

package sessionpressure

import "fmt"

func nativePhysicalMemoryMB() (float64, error) {
	return 0, fmt.Errorf("native physical memory sampler unavailable")
}

func nativeSwapUsedMB() (float64, error) {
	return 0, fmt.Errorf("native swap sampler unavailable")
}
