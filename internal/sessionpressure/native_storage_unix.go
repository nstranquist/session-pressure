//go:build darwin || linux

package sessionpressure

import "golang.org/x/sys/unix"

func nativeStorageCapacity(path string) (StorageCapacity, error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return StorageCapacity{}, err
	}
	blockSize := int64(stats.Bsize)
	return StorageCapacity{
		TotalBytes:     saturatingBlockBytes(uint64(stats.Blocks), blockSize),
		FreeBytes:      saturatingBlockBytes(uint64(stats.Bfree), blockSize),
		AvailableBytes: saturatingBlockBytes(uint64(stats.Bavail), blockSize),
	}, nil
}
