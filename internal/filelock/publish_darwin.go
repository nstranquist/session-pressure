package filelock

import "golang.org/x/sys/unix"

// platformExclusiveRename moves from to to only if to does not exist
// (renamex_np RENAME_EXCL). It fails with EEXIST when to exists.
var platformExclusiveRename = func(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}
