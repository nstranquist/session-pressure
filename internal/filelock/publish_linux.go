package filelock

import "golang.org/x/sys/unix"

// platformExclusiveRename moves from to to only if to does not exist
// (renameat2 RENAME_NOREPLACE). It fails with EEXIST when to exists.
var platformExclusiveRename = func(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}
