//go:build !windows && !darwin && !linux

package bridge

import "errors"

// A two-step link/remove can delete a concurrent replacement at from.
// Refuse until this platform has an atomic exclusive rename implementation.
func moveNoReplace(from, to string) error {
	return errors.ErrUnsupported
}
