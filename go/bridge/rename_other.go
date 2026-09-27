//go:build !linux && !darwin && !windows

package bridge

import "errors"

func moveDirectoryNoReplace(from, to string) error {
	return errors.ErrUnsupported
}
