package bridge

import "golang.org/x/sys/unix"

// Atomic exclusive rename keeps the source and destination names consistent
// when another writer replaces either name. It preserves a symbolic link's
// inode identity for the ownership receipt.
func moveNoReplace(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}
