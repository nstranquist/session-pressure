//go:build !unix

package sessionpressure

import "os"

// remoteSourceInode is unavailable here; size and mtime still guard the cache.
func remoteSourceInode(os.FileInfo) uint64 { return 0 }
