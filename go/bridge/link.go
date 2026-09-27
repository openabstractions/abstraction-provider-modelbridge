package bridge

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

// ErrLinkRefused is every way this machine can decline to place a link: the
// operating system refusing this process the symbolic-link privilege, an
// engine refusing to follow one, and a hard link across volumes.
var ErrLinkRefused = errors.New("modelbridge: neither a symbolic nor a hard link can be placed")

// Linker places one link. The default is the operating system; a test
// substitutes each half to reach the cases a machine will not produce on
// demand. DECISION.md rule 2, amended by the 2026-09-22 experiment: the
// symbolic link is the default, the hard link is the fallback when the
// operating system refuses this process the privilege, and a copy is made only
// when the caller asks for it.
type Linker struct {
	Symlink    func(source, link string) error
	Hardlink   func(source, link string) error
	SameVolume func(source, link string) (bool, error)
	// SymlinksRefused, when true, skips the symbolic link: an engine that does
	// not follow one is told about here and not discovered per file.
	SymlinksRefused bool
}

// OSLinker links through the operating system.
func OSLinker() Linker {
	return Linker{Symlink: os.Symlink, Hardlink: os.Link, SameVolume: SameVolume}
}

// Place puts source at link. It returns the kind it managed. A copy is made
// only when copy is true and no link can be placed; otherwise the refusal is
// ErrLinkRefused.
func (l Linker) Place(source, link string, copy bool) (wire.Link, error) {
	if !l.SymlinksRefused && l.Symlink != nil {
		if err := l.Symlink(source, link); err == nil {
			return wire.LinkSymbolic, nil
		}
		// A link that already exists is the caller's mistake, not a refusal.
		if _, err := os.Lstat(link); err == nil {
			return 0, os.ErrExist
		}
	}
	same, err := true, error(nil)
	if l.SameVolume != nil {
		same, err = l.SameVolume(source, link)
		if err != nil {
			return 0, err
		}
	}
	if same && l.Hardlink != nil {
		if err := l.Hardlink(source, link); err == nil {
			return wire.LinkHard, nil
		}
		if _, err := os.Lstat(link); err == nil {
			return 0, os.ErrExist
		}
	}
	if !copy {
		return 0, ErrLinkRefused
	}
	if err := copyFile(source, link); err != nil {
		return 0, err
	}
	return wire.LinkCopy, nil
}

// SameVolume reports whether two paths can hold a hard link between them: the
// same volume on Windows, the same device elsewhere. link need not exist; its
// directory does.
func SameVolume(source, link string) (bool, error) {
	a, err := filepath.Abs(source)
	if err != nil {
		return false, err
	}
	b, err := filepath.Abs(link)
	if err != nil {
		return false, err
	}
	return sameVolume(a, filepath.Dir(b))
}

// copyFile writes a whole copy, which a lend counts as a copy and never as a
// link. It refuses to overwrite.
func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, copyErr := io.Copy(out, in); copyErr != nil {
		created, statErr := out.Stat()
		closeErr := out.Close()
		return failedCopy(target, created, errors.Join(copyErr, statErr, closeErr))
	}
	created, statErr := out.Stat()
	if err := out.Close(); err != nil {
		return failedCopy(target, created, errors.Join(err, statErr))
	}
	return nil
}

func failedCopy(target string, created os.FileInfo, primary error) error {
	if created == nil {
		return primary
	}
	_, cleanupErr := removeSameFile(target, created)
	return errors.Join(primary, cleanupErr)
}
