//go:build !darwin && !linux

package sessionpressure

import "errors"

func nativeStorageCapacity(string) (StorageCapacity, error) {
	return StorageCapacity{}, errors.New("native filesystem capacity sampling is unsupported on this platform")
}
