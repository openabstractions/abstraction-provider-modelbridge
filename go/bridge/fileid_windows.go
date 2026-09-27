package bridge

import (
	"os"
	"syscall"
)

// fileID names the filesystem object itself, including a symbolic link rather
// than its target. The pair survives a service restart.
type fileID struct {
	Volume uint64 `json:"volume"`
	Index  uint64 `json:"index"`
}

func identityOf(path string) (fileID, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fileID{}, err
	}
	h, err := syscall.CreateFile(name, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return fileID{}, err
	}
	defer syscall.CloseHandle(h)
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return fileID{}, err
	}
	return fileID{Volume: uint64(info.VolumeSerialNumber), Index: uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}, nil
}

func moveNoReplace(from, to string) error {
	a, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	b, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	if err := syscall.MoveFile(a, b); err != nil {
		if _, statErr := os.Lstat(to); statErr == nil {
			return os.ErrExist
		}
		return err
	}
	return nil
}
