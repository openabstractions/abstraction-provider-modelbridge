//go:build windows

package bridge

import (
	"path/filepath"
	"strings"
)

// sameVolume compares the volume names Windows gives two absolute paths. A
// hard link between two volumes is refused by the file system, so the bridge
// answers link_refused rather than making a copy nobody asked for.
func sameVolume(source, dir string) (bool, error) {
	return strings.EqualFold(filepath.VolumeName(source), filepath.VolumeName(dir)), nil
}
