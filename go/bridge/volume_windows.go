//go:build windows

package bridge

import (
	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
)

// sameVolume reports whether two existing paths sit on one volume: the mount
// point each lies under (GetVolumePathName, which also sees a volume mounted
// in a folder) is one folder by file identity (identity.SameFile). A hard
// link between two volumes is refused by the file system, so the bridge
// answers link_refused rather than making a copy nobody asked for.
func sameVolume(source, dir string) (bool, error) {
	a, err := volumeRoot(source)
	if err != nil {
		return false, err
	}
	b, err := volumeRoot(dir)
	if err != nil {
		return false, err
	}
	return identity.SameFile(a, b)
}

func volumeRoot(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	if err := windows.GetVolumePathName(p, &buf[0], uint32(len(buf))); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf), nil
}
