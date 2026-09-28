package bridge

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type fileID struct {
	Volume       uint64 `json:"volume"`
	Index        uint64 `json:"index"`
	Version      uint8  `json:"version"`
	BirthSeconds int64  `json:"birth_seconds"`
	BirthNanos   int64  `json:"birth_nanos"`
}

// A receipt requires birth time: device and inode alone can name a replacement
// after the original object has been removed. Version rejects older receipts.
func identityOf(path string) (fileID, error) {
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW,
		unix.STATX_BASIC_STATS|unix.STATX_BTIME, &stat); err != nil {
		return fileID{}, fmt.Errorf("modelbridge: inspect filesystem identity: %w", err)
	}
	if stat.Mask&unix.STATX_BTIME == 0 {
		return fileID{}, errors.New("modelbridge: filesystem birth time unavailable for ownership receipt")
	}
	return fileID{
		Volume: unix.Mkdev(stat.Dev_major, stat.Dev_minor), Index: stat.Ino, Version: 1,
		BirthSeconds: stat.Btime.Sec, BirthNanos: int64(stat.Btime.Nsec),
	}, nil
}
