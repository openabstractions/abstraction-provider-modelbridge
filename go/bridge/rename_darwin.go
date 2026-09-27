package bridge

import "golang.org/x/sys/unix"

func moveDirectoryNoReplace(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}

// Rename the symbolic link itself, preserving the receipt's filesystem identity.
func moveNoReplace(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}
