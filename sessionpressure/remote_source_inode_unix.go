//go:build unix

package sessionpressure

import (
	"os"
	"syscall"
)

// remoteSourceInode is the file's inode, part of the stat-cache identity: a
// file replaced by rename keeps size and can keep mtime, but not its inode.
func remoteSourceInode(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
