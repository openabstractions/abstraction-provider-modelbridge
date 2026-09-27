//go:build !windows

package bridge

import (
	"os"
	"syscall"
)

// sameVolume compares the devices two absolute paths sit on. A hard link
// across devices is refused by the kernel, so the bridge answers link_refused
// rather than making a copy nobody asked for.
func sameVolume(source, dir string) (bool, error) {
	a, err := os.Stat(source)
	if err != nil {
		return false, err
	}
	b, err := os.Stat(dir)
	if err != nil {
		return false, err
	}
	sa, oka := a.Sys().(*syscall.Stat_t)
	sb, okb := b.Sys().(*syscall.Stat_t)
	if !oka || !okb {
		return true, nil
	}
	return sa.Dev == sb.Dev, nil
}
