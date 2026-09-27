//go:build !windows

package bridge

import "syscall"

type fileID struct {
	Volume uint64 `json:"volume"`
	Index  uint64 `json:"index"`
}

func identityOf(path string) (fileID, error) {
	var stat syscall.Stat_t
	if err := syscall.Lstat(path, &stat); err != nil {
		return fileID{}, err
	}
	return fileID{Volume: uint64(stat.Dev), Index: uint64(stat.Ino)}, nil
}
