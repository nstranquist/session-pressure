//go:build !darwin

package sessionpressure

import "errors"

func nativeHostCPUTicks() (hostCPUTicks, error) {
	return hostCPUTicks{}, errors.New("native host CPU sampling unavailable")
}
