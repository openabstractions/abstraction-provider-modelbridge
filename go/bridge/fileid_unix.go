//go:build darwin

package bridge

import "syscall"

type fileID struct {
	Volume       uint64 `json:"volume"`
	Index        uint64 `json:"index"`
	Version      uint8  `json:"version"`
	BirthSeconds int64  `json:"birth_seconds"`
	BirthNanos   int64  `json:"birth_nanos"`
}

func identityOf(path string) (fileID, error) {
	var stat syscall.Stat_t
	if err := syscall.Lstat(path, &stat); err != nil {
		return fileID{}, err
	}
	return fileID{
		Volume: uint64(stat.Dev), Index: uint64(stat.Ino), Version: 1,
		BirthSeconds: stat.Birthtimespec.Sec, BirthNanos: stat.Birthtimespec.Nsec,
	}, nil
}
