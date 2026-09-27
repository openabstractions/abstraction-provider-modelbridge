package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"

	cas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

// ledgerVersion is the record version on disk. A file of another version is
// refused rather than reinterpreted.
const ledgerVersion = 1

// MaxLends bounds the ledger. A machine with more borrowed files than this has
// a different problem.
const MaxLends = 1024

// ledgerFile is the ledger's shape on disk.
type ledgerFile struct {
	Version int                  `json:"version"`
	Lends   []wire.Lend          `json:"lends"`
	Owned   map[string]ownedLink `json:"owned,omitempty"`
	Dirs    map[string]fileID    `json:"dirs,omitempty"`
	Pending []lendIntent         `json:"pending,omitempty"`
}

// lendIntent is durable before a file is placed. A recorded identity means a
// restart may finish the transition from staging name to ledger entry.
type lendIntent struct {
	Entry    wire.Lend `json:"entry"`
	Stage    string    `json:"stage"`
	File     fileID    `json:"file"`
	Parent   fileID    `json:"parent"`
	Creating string    `json:"creating,omitempty"`
}

type ownedLink struct {
	File   fileID `json:"file"`
	Parent fileID `json:"parent"`
}

// Ledger is what this provider wrote into other applications' directories. It
// is the answer to whether a lend is in force: an engine's own list may keep
// naming a model until that engine's idle-unload timer fires.
type Ledger struct {
	mu      sync.Mutex
	path    string
	lends   []wire.Lend
	owned   map[string]ownedLink
	dirs    map[string]fileID
	pending []lendIntent
}

// OpenLedger reads the ledger at path, or starts an empty one.
func OpenLedger(path string) (*Ledger, error) {
	l := &Ledger{path: path, lends: []wire.Lend{}, owned: map[string]ownedLink{}, dirs: map[string]fileID{}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return l, nil
	case err != nil:
		return nil, err
	}
	var file ledgerFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	if file.Version != ledgerVersion {
		return nil, errors.New("modelbridge: the ledger is not a version 1 record")
	}
	if file.Lends != nil {
		l.lends = file.Lends
	}
	if file.Owned != nil {
		l.owned = file.Owned
	}
	if file.Dirs != nil {
		l.dirs = file.Dirs
	}
	l.pending = file.Pending
	if err := l.recoverQuarantines(); err != nil {
		return nil, err
	}
	if err := l.recoverPending(); err != nil {
		return nil, err
	}
	if err := l.pruneOwnedDirs(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Ledger) recoverQuarantines() error {
	for _, intent := range l.pending {
		if intent.File == (fileID{}) {
			continue
		}
		if err := checkParent(intent.Entry.Link, intent.Parent); err != nil {
			return err
		}
		if err := recoverQuarantine(intent.Stage, intent.File, false); err != nil {
			return err
		}
		if err := recoverQuarantine(intent.Entry.Link, intent.File, false); err != nil {
			return err
		}
	}
	for _, entry := range l.lends {
		if ownership, ok := l.owned[entry.ID]; ok {
			if err := checkParent(entry.Link, ownership.Parent); err != nil {
				return err
			}
			if err := recoverQuarantine(entry.Link, ownership.File, false); err != nil {
				return err
			}
		}
	}
	for path, identity := range l.dirs {
		if err := recoverQuarantine(path, identity, true); err != nil {
			return err
		}
	}
	return nil
}

func checkParent(path string, expected fileID) error {
	if expected == (fileID{}) {
		return errors.New("modelbridge: lend has no directory ownership record")
	}
	parent := filepath.Dir(path)
	identity, err := identityOf(parent)
	if err != nil || identity != expected {
		return errors.New("modelbridge: lend directory changed: " + parent)
	}
	return nil
}

func (l *Ledger) recoverPending() error {
	for len(l.pending) > 0 {
		intent := l.pending[0]
		if intent.Creating != "" {
			if _, err := os.Lstat(intent.Creating); err == nil {
				return errors.New("modelbridge: unfinished created directory needs reconciliation: " + intent.Creating)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			q, err := pendingCreationQuarantine(intent.Creating)
			if err != nil {
				return err
			}
			if q != "" {
				return errors.New("modelbridge: unfinished created directory quarantine needs reconciliation: " + q)
			}
		}
		if intent.Parent != (fileID{}) {
			parent, err := identityOf(filepath.Dir(intent.Entry.Link))
			if err != nil || parent != intent.Parent {
				return errors.New("modelbridge: unfinished lend directory changed: " + filepath.Dir(intent.Entry.Link))
			}
		}
		if intent.File == (fileID{}) {
			if _, err := os.Lstat(intent.Stage); err == nil {
				return errors.New("modelbridge: unfinished staged lend needs reconciliation: " + intent.Stage)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if _, err := os.Lstat(intent.Entry.Link); err == nil {
				return errors.New("modelbridge: unfinished lend needs reconciliation: " + intent.Entry.Link)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			before := l.pending
			l.pending = l.pending[1:]
			if err := l.write(); err != nil {
				l.pending = before
				return err
			}
			continue
		}
		if id, err := identityOf(intent.Entry.Link); err == nil && id == intent.File {
			// The stage can be a second hard-link name for the placed file.
			// Keep the intent durable until that name is gone: a failed cleanup
			// must still have an ownership record for the next recovery.
			if stageID, stageErr := identityOf(intent.Stage); stageErr == nil {
				if stageID != intent.File {
					return errors.New("modelbridge: staged lend was replaced: " + intent.Stage)
				}
				if _, err := removeOwned(intent.Stage, stageID, false); err != nil {
					return err
				}
			} else if !errors.Is(stageErr, fs.ErrNotExist) {
				return stageErr
			}
			before := l.pending
			priorOwnership, hadOwnership := l.owned[intent.Entry.ID]
			l.lends = append(l.lends, intent.Entry)
			l.owned[intent.Entry.ID] = ownedLink{File: intent.File, Parent: intent.Parent}
			l.pending = l.pending[1:]
			if err := l.write(); err != nil {
				l.pending = before
				l.lends = l.lends[:len(l.lends)-1]
				if hadOwnership {
					l.owned[intent.Entry.ID] = priorOwnership
				} else {
					delete(l.owned, intent.Entry.ID)
				}
				return err
			}
			continue
		}
		if id, err := identityOf(intent.Stage); err == nil && id == intent.File {
			if _, err := removeOwned(intent.Stage, id, false); err != nil {
				return err
			}
		} else if err == nil {
			return errors.New("modelbridge: staged lend was replaced: " + intent.Stage)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if _, err := os.Lstat(intent.Entry.Link); err == nil {
			return errors.New("modelbridge: unfinished lend path was replaced: " + intent.Entry.Link)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		before := l.pending
		l.pending = l.pending[1:]
		if err := l.write(); err != nil {
			l.pending = before
			return err
		}
	}
	return nil
}

func (l *Ledger) begin(intent lendIntent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lends)+len(l.pending) >= MaxLends {
		return errors.New("modelbridge: the ledger is full")
	}
	l.pending = append(l.pending, intent)
	if err := l.write(); err != nil {
		l.pending = l.pending[:len(l.pending)-1]
		return err
	}
	return nil
}

func (l *Ledger) placed(id string, kind wire.Link, identity, parent fileID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.pending {
		if l.pending[i].Entry.ID == id {
			before := l.pending[i]
			l.pending[i].Entry.Kind = kind
			l.pending[i].File = identity
			l.pending[i].Parent = parent
			if err := l.write(); err != nil {
				l.pending[i] = before
				return err
			}
			return nil
		}
	}
	return errors.New("modelbridge: missing lend intent")
}

func (l *Ledger) commit(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, intent := range l.pending {
		if intent.Entry.ID == id {
			if len(l.lends) >= MaxLends {
				return errors.New("modelbridge: the ledger is full")
			}
			old := slices.Clone(l.pending)
			l.pending = slices.Delete(l.pending, i, i+1)
			l.lends = append(l.lends, intent.Entry)
			l.owned[id] = ownedLink{File: intent.File, Parent: intent.Parent}
			if err := l.write(); err != nil {
				l.pending = old
				l.lends = l.lends[:len(l.lends)-1]
				delete(l.owned, id)
				return err
			}
			return nil
		}
	}
	return errors.New("modelbridge: missing lend intent")
}

func (l *Ledger) abort(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, intent := range l.pending {
		if intent.Entry.ID == id {
			l.pending = slices.Delete(l.pending, i, i+1)
			if err := l.write(); err != nil {
				l.pending = slices.Insert(l.pending, i, intent)
				return err
			}
			return nil
		}
	}
	return nil
}

// markCreating records a path before mkdir can create it. A restart cannot
// claim an existing path as ours while this marker lacks a native identity.
func (l *Ledger) markCreating(id, path string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.pending {
		if l.pending[i].Entry.ID == id {
			if l.pending[i].Creating != "" {
				return errors.Join(errDirectoryCleanupUncertain,
					errors.New("modelbridge: an unfinished directory already needs reconciliation"))
			}
			l.pending[i].Creating = path
			if err := l.write(); err != nil {
				l.pending[i].Creating = ""
				return err
			}
			return nil
		}
	}
	return errors.New("modelbridge: missing lend intent")
}

func (l *Ledger) clearCreating(id, path string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.pending {
		if l.pending[i].Entry.ID == id && l.pending[i].Creating == path {
			l.pending[i].Creating = ""
			if err := l.write(); err != nil {
				l.pending[i].Creating = path
				return err
			}
			return nil
		}
	}
	return errors.New("modelbridge: missing directory creation intent")
}

func (l *Ledger) trackDir(id, path string, identity fileID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.pending {
		if l.pending[i].Entry.ID == id && l.pending[i].Creating == path {
			previous, hadPrevious := l.dirs[path]
			l.dirs[path] = identity
			l.pending[i].Creating = ""
			if err := l.write(); err != nil {
				l.pending[i].Creating = path
				if hadPrevious {
					l.dirs[path] = previous
				} else {
					delete(l.dirs, path)
				}
				return err
			}
			return nil
		}
	}
	return errors.New("modelbridge: missing directory creation intent")
}

func (l *Ledger) ownedFile(id string) (ownedLink, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	identity, ok := l.owned[id]
	return identity, ok
}

func (l *Ledger) pruneOwnedDirs() error {
	// A directory can be shared by several lends. Keep it in the ownership
	// record until it becomes empty, regardless of which entry created it.
	paths := make([]string, 0, len(l.dirs))
	for path := range l.dirs {
		paths = append(paths, path)
	}
	slices.SortFunc(paths, func(a, b string) int { return len(b) - len(a) })
	var problems []error
	for _, path := range paths {
		entries, err := os.ReadDir(path)
		if errors.Is(err, fs.ErrNotExist) {
			identity := l.dirs[path]
			delete(l.dirs, path)
			if err := l.write(); err != nil {
				l.dirs[path] = identity
				problems = append(problems, fmt.Errorf("modelbridge: record removed directory %s: %w", path, err))
			}
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("modelbridge: read owned directory %s: %w", path, err))
			continue
		}
		if len(entries) != 0 {
			continue
		}
		removed, err := removeOwned(path, l.dirs[path], true)
		if err != nil {
			problems = append(problems, fmt.Errorf("modelbridge: prune owned directory %s: %w", path, err))
			continue
		}
		if removed {
			identity := l.dirs[path]
			delete(l.dirs, path)
			if err := l.write(); err != nil {
				l.dirs[path] = identity
				problems = append(problems, fmt.Errorf("modelbridge: record pruned directory %s: %w", path, err))
			}
		}
	}
	return errors.Join(problems...)
}

// All is every entry, oldest first.
func (l *Ledger) All() []wire.Lend {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lends)
}

// Find returns the entry id names.
func (l *Ledger) Find(id string) (wire.Lend, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := slices.IndexFunc(l.lends, func(e wire.Lend) bool { return e.ID == id })
	if i < 0 {
		return wire.Lend{}, false
	}
	return l.lends[i], true
}

// InForce returns the entry that already lends this object to this engine.
func (l *Ledger) InForce(engine, store, object string) (wire.Lend, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := slices.IndexFunc(l.lends, func(e wire.Lend) bool {
		return e.Engine == engine && e.Store == store && e.Object == object
	})
	if i < 0 {
		return wire.Lend{}, false
	}
	return l.lends[i], true
}

func (l *Ledger) pendingAt(engine, store, object, link string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.ContainsFunc(l.pending, func(intent lendIntent) bool {
		e := intent.Entry
		return e.Link == link || e.Engine == engine && e.Store == store && e.Object == object
	})
}

// Add appends one entry and writes the ledger before the caller is told.
func (l *Ledger) Add(entry wire.Lend) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lends) >= MaxLends {
		return errors.New("modelbridge: the ledger is full")
	}
	l.lends = append(l.lends, entry)
	if err := l.write(); err != nil {
		l.lends = l.lends[:len(l.lends)-1]
		return err
	}
	return nil
}

// Remove drops one entry and writes the ledger.
func (l *Ledger) Remove(id string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := slices.IndexFunc(l.lends, func(e wire.Lend) bool { return e.ID == id })
	if i < 0 {
		return false, nil
	}
	kept := slices.Clone(l.lends)
	identity, wasOwned := l.owned[id]
	l.lends = slices.Delete(l.lends, i, i+1)
	delete(l.owned, id)
	if err := l.write(); err != nil {
		l.lends = kept
		if wasOwned {
			l.owned[id] = identity
		}
		return false, err
	}
	return true, nil
}

// write replaces the file in one rename, so a reader sees the whole ledger or
// the one before it.
func (l *Ledger) write() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	return cas.Change(l.path, func([]byte) ([]byte, error) {
		raw, err := json.MarshalIndent(ledgerFile{Version: ledgerVersion, Lends: l.lends, Owned: l.owned, Dirs: l.dirs, Pending: l.pending}, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(raw, '\n'), nil
	})
}

// NewEntryID names one ledger entry.
func NewEntryID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "lend-" + hex.EncodeToString(b[:]), nil
}
