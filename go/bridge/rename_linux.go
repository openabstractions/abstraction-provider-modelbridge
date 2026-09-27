package bridge

import "golang.org/x/sys/unix"

// Preserve both directories if another writer reserves the destination.
// A filesystem without exclusive rename support must refuse this operation.
func moveDirectoryNoReplace(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}
