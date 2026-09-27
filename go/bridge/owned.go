package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// removeOwned moves a name out of the engine's tree before checking identity.
// A replacement is restored when the old name is free. If another writer
// fills that name meanwhile, the replacement remains at the reported
// quarantine path for the operator to reconcile; it is never deleted.
func removeOwned(path string, expected fileID, directory bool) (bool, error) {
	if expected == (fileID{}) {
		return false, errors.New("modelbridge: no file ownership record")
	}
	if err := recoverQuarantine(path, expected, directory); err != nil {
		return false, err
	}
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	q := quarantineDir(path, expected)
	if err := os.Mkdir(q, 0o700); err != nil {
		return false, err
	}
	quarantined := filepath.Join(q, filepath.Base(path))
	var err error
	if directory {
		err = moveDirectoryNoReplace(path, quarantined)
	} else {
		err = moveNoReplace(path, quarantined)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return finishQuarantine(q, false, nil)
	}
	if err != nil {
		return finishQuarantine(q, false, err)
	}
	identity, err := identityOf(quarantined)
	var primary error
	switch {
	case err != nil:
		primary = fmt.Errorf("modelbridge: inspect quarantined path %s: %w", quarantined, err)
	case identity != expected:
		primary = errors.New("modelbridge: path is no longer the lend this service placed")
	default:
		if err := os.Remove(quarantined); err == nil {
			return finishQuarantine(q, true, nil)
		} else {
			primary = fmt.Errorf("modelbridge: remove owned path %s: %w", quarantined, err)
		}
	}
	if restoreErr := moveBack(quarantined, path, directory); restoreErr != nil {
		return false, errors.Join(primary, fmt.Errorf("modelbridge: retained at %s for reconciliation: %w", quarantined, restoreErr))
	}
	return finishQuarantine(q, false, primary)
}

// finishQuarantine reports the completed object removal even if cleaning its
// empty holding directory fails. The caller retains the operation error too.
func finishQuarantine(q string, removed bool, primary error) (bool, error) {
	if err := os.Remove(q); err != nil {
		return removed, errors.Join(primary, fmt.Errorf("modelbridge: remove quarantine %s: %w", q, err))
	}
	return removed, primary
}

func quarantineDir(path string, expected fileID) string {
	name := fmt.Sprintf(".openabstractions-undo-%016x-%016x-%s", expected.Volume, expected.Index, quarantinePathTag(path))
	return filepath.Join(filepath.Dir(path), name)
}

func quarantinePathTag(path string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(digest[:6])
}

// A pre-creation intent has no durable file identity. It can still detect an
// interrupted owned cleanup by the original path's deterministic quarantine
// tag. A matching name is left untouched for operator inspection.
func pendingCreationQuarantine(path string) (string, error) {
	parent := filepath.Dir(path)
	entries, err := os.ReadDir(parent)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	suffix := "-" + quarantinePathTag(path)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".openabstractions-undo-") && strings.HasSuffix(name, suffix) {
			return filepath.Join(parent, name), nil
		}
	}
	return "", nil
}

// recoverQuarantine finishes or restores an interrupted undo using only the
// ownership already recorded in the ledger or intent. Its name is derived
// from that record, so a restart does not need to scan an engine directory.
func recoverQuarantine(path string, expected fileID, directory bool) error {
	q := quarantineDir(path, expected)
	quarantined := filepath.Join(q, filepath.Base(path))
	if _, err := os.Lstat(q); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	identity, err := identityOf(quarantined)
	if errors.Is(err, fs.ErrNotExist) {
		return os.Remove(q)
	}
	if err != nil {
		return err
	}
	if identity == expected {
		if err := os.Remove(quarantined); err != nil {
			return err
		}
		return os.Remove(q)
	}
	if err := moveBack(quarantined, path, directory); err != nil {
		return fmt.Errorf("modelbridge: foreign replacement retained at %s for reconciliation: %w", quarantined, err)
	}
	if err := os.Remove(q); err != nil {
		return err
	}
	return errors.New("modelbridge: foreign replacement restored after interrupted undo")
}

func moveBack(from, to string, directory bool) error {
	if directory {
		return moveDirectoryNoReplace(from, to)
	}
	return moveNoReplace(from, to)
}

// removeSameFile cleans an interrupted copy using the open file's identity.
// It leaves a file another writer put at the same path untouched.
func removeSameFile(path string, created os.FileInfo) (bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	q, err := os.MkdirTemp(filepath.Dir(path), ".openabstractions-undo-")
	if err != nil {
		return false, err
	}
	quarantined := filepath.Join(q, filepath.Base(path))
	if err := moveNoReplace(path, quarantined); err != nil {
		return finishQuarantine(q, false, err)
	}
	info, err := os.Lstat(quarantined)
	var primary error
	switch {
	case err != nil:
		primary = fmt.Errorf("modelbridge: inspect incomplete copy %s: %w", quarantined, err)
	case !os.SameFile(created, info):
		primary = errors.New("modelbridge: incomplete copy was replaced")
	default:
		if err := os.Remove(quarantined); err == nil {
			return finishQuarantine(q, true, nil)
		} else {
			primary = fmt.Errorf("modelbridge: remove incomplete copy %s: %w", quarantined, err)
		}
	}
	if restoreErr := moveBack(quarantined, path, false); restoreErr != nil {
		return false, errors.Join(primary, fmt.Errorf("modelbridge: incomplete copy retained at %s: %w", quarantined, restoreErr))
	}
	return finishQuarantine(q, false, primary)
}
