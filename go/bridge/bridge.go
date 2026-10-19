package bridge

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

// MaxPage is the largest page Lends answers, as the contract states.
const MaxPage = 256

// ReasonPickle opens the detail of a lend refused because its object is a
// PyTorch pickle (MODELBRIDGE-L20).
const ReasonPickle = "object:pickle"

// Bridge is the provider's one operation: lend a file a store holds to an
// engine installed on this machine, and take the lend back.
type Bridge struct {
	mu      sync.Mutex
	source  Source
	ledger  *Ledger
	engines []Engine
	linker  Linker
	// now stamps entries; nil uses the clock.
	now func() time.Time
}

// New composes a bridge over one source, one ledger and the engines this
// machine has.
func New(src Source, ledger *Ledger, linker Linker, engines ...Engine) *Bridge {
	return &Bridge{source: src, ledger: ledger, engines: engines, linker: linker}
}

// Engines is every engine name this provider supports, in declaration order.
func (b *Bridge) Engines() []string {
	out := make([]string, 0, len(b.engines))
	for _, e := range b.engines {
		out = append(out, e.Name())
	}
	return out
}

func (b *Bridge) engine(name string) (Engine, bool) {
	i := slices.IndexFunc(b.engines, func(e Engine) bool { return e.Name() == name })
	if i < 0 {
		return nil, false
	}
	return b.engines[i], true
}

func (b *Bridge) stamp() string {
	at := time.Now
	if b.now != nil {
		at = b.now
	}
	return at().UTC().Format("2006-01-02T15:04:05.000000Z")
}

func refuseLend(outcome wire.LendOutcome, detail string) wire.LendResult {
	return wire.LendResult{Outcome: outcome, Detail: detail}
}

func cleanupRefusal(detail string, primary, cleanup error) wire.LendResult {
	if cleanup != nil {
		return refuseLend(wire.LendOutcomeUnavailable, detail+": "+errors.Join(primary, cleanup).Error())
	}
	return refuseLend(wire.LendOutcomeUnavailable, detail)
}

func (b *Bridge) abortAndPrune(id string) error {
	if err := b.ledger.abort(id); err != nil {
		return fmt.Errorf("modelbridge: retain unfinished lend intent: %w", err)
	}
	return b.ledger.pruneOwnedDirs()
}

var unsafeName = strings.NewReplacer("/", "-", "\\", "-", ":", "-")

var errDirectoryCleanupUncertain = errors.New("modelbridge: created directory needs reconciliation")

// linkName is what a lend calls the file inside the engine's directory. It
// keeps the source file's own name, so the engine derives what it derives from
// it. A store whose file names say nothing — Jan writes every model as
// model.gguf — lends under the family instead: LM Studio, measured on
// 2026-09-22, indexed sentence-transformer-mini.gguf within eight seconds and
// skipped the identical model.gguf in the same directory entirely.
func linkName(o Object) string {
	base := unsafeName.Replace(o.File)
	if !generic(Family(base)) {
		return base
	}
	return o.Family + path.Ext(base)
}

// Lend places store's object in engine's own directory by link. It never
// moves, renames or deletes the original.
func (b *Bridge) Lend(store, id, engineName string, copy bool) wire.LendResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case store == "" || id == "" || engineName == "":
		return refuseLend(wire.LendOutcomeInvalid, "store, object and engine are each required")
	case len(store) > 256 || len(id) > 1024 || len(engineName) > 64:
		return refuseLend(wire.LendOutcomeInvalid, "store, object or engine is too long")
	}
	engine, known := b.engine(engineName)
	if !known {
		return refuseLend(wire.LendOutcomeUnsupportedEngine,
			fmt.Sprintf("%s is not an engine this provider lends to; it lends to %s", engineName, strings.Join(b.Engines(), ", ")))
	}
	// A lend is a link, and a link names a whole file (MODELBRIDGE-L3,
	// MODELBRIDGE-L19).
	object, err := b.source.Resolve(store, id, FileOnly)
	if errors.Is(err, ErrPickle) {
		return refuseLend(wire.LendOutcomeNotPermitted, fmt.Sprintf(
			"%s: %s in store %s is a PyTorch pickle, which OA never loads or lends", ReasonPickle, id, store))
	}
	if errors.Is(err, ErrUnaccepted) {
		return refuseLend(wire.LendOutcomeLinkRefused, fmt.Sprintf(
			"%s in store %s lies inside a container: a link cannot point into one, and this provider makes no copy of a member", id, store))
	}
	if err != nil {
		return refuseLend(wire.LendOutcomeUnknownObject, fmt.Sprintf("no object %s in store %s", id, store))
	}
	if entry, lent := b.ledger.InForce(engineName, object.Store, lentAs(object)); lent {
		ownership, owned := b.ledger.ownedFile(entry.ID)
		if !owned || ownership.File == (fileID{}) || checkParent(entry.Link, ownership.Parent) != nil {
			return refuseLend(wire.LendOutcomeUnavailable, "the existing lend needs reconciliation")
		}
		identity, err := identityOf(entry.Link)
		if err != nil || identity != ownership.File {
			return refuseLend(wire.LendOutcomeUnavailable, "the existing lend needs reconciliation")
		}
		return wire.LendResult{Outcome: wire.LendOutcomeAlreadyLent, Lend: &entry, Detail: "already lent to " + engineName}
	}
	root, err := engine.Root(object)
	if err != nil {
		return refuseLend(wire.LendOutcomeUnsupportedEngine, err.Error())
	}
	info, err := os.Stat(object.Path)
	if err != nil {
		return refuseLend(wire.LendOutcomeUnknownObject, fmt.Sprintf("the file store %s holds for %s is not readable", store, id))
	}
	if err := engine.Accept(root); err != nil {
		return refuseLend(wire.LendOutcomeUnsupportedEngine, err.Error())
	}
	directory := filepath.Join(root, object.Family)
	link := filepath.Join(directory, linkName(object))
	if b.ledger.pendingAt(engineName, object.Store, lentAs(object), link) {
		return refuseLend(wire.LendOutcomeUnavailable, "an unfinished lend needs reconciliation")
	}
	if _, err := os.Lstat(link); err == nil {
		return refuseLend(wire.LendOutcomeLinkRefused, "an unrecorded file occupies the destination")
	} else if !errors.Is(err, os.ErrNotExist) {
		return refuseLend(wire.LendOutcomeUnavailable, "the engine's directory cannot be read")
	}
	entry := wire.Lend{ID: "", Engine: engineName, Store: object.Store, Object: lentAs(object), Name: object.Family,
		Link: link, Kind: wire.LinkSymbolic, Size: info.Size(), Created: b.stamp()}
	entry.ID, err = NewEntryID()
	if err != nil {
		return refuseLend(wire.LendOutcomeUnavailable, "the entry could not be named")
	}
	stage := filepath.Join(directory, ".openabstractions-stage-"+entry.ID)
	if err := b.ledger.begin(lendIntent{Entry: entry, Stage: stage}); err != nil {
		return refuseLend(wire.LendOutcomeUnavailable, "the lend intent could not be written")
	}
	if err := b.ensureDirectory(entry.ID, directory); err != nil {
		if errors.Is(err, errDirectoryCleanupUncertain) {
			return refuseLend(wire.LendOutcomeUnavailable, "the engine's directory needs reconciliation: "+err.Error())
		}
		return cleanupRefusal("the engine's directory cannot be written", err, b.abortAndPrune(entry.ID))
	}
	parent, err := identityOf(directory)
	if err != nil {
		return cleanupRefusal("the engine's directory identity could not be read", err, b.abortAndPrune(entry.ID))
	}
	kind, err := b.linker.Place(object.Path, stage, copy)
	if err != nil {
		if _, statErr := os.Lstat(stage); statErr == nil {
			return refuseLend(wire.LendOutcomeUnavailable, "an unfinished staged file needs reconciliation")
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return refuseLend(wire.LendOutcomeUnavailable, "the staged file could not be checked")
		}
		if cleanupErr := b.abortAndPrune(entry.ID); cleanupErr != nil {
			return cleanupRefusal("the failed placement needs reconciliation", err, cleanupErr)
		}
		if errors.Is(err, ErrLinkRefused) {
			return refuseLend(wire.LendOutcomeLinkRefused,
				"this machine allows neither a symbolic link here nor a hard link between these volumes")
		}
		return refuseLend(wire.LendOutcomeUnavailable, "the link could not be placed")
	}
	identity, err := identityOf(stage)
	if err != nil {
		return refuseLend(wire.LendOutcomeUnavailable, "the staged link identity could not be read; its intent needs reconciliation")
	}
	if err := b.ledger.placed(entry.ID, kind, identity, parent); err != nil {
		_, cleanupErr := removeOwned(stage, identity, false)
		if cleanupErr == nil {
			cleanupErr = b.abortAndPrune(entry.ID)
		}
		return cleanupRefusal("the staged link identity could not be recorded; its intent needs reconciliation", err, cleanupErr)
	}
	entry.Kind = kind
	if err := moveNoReplace(stage, link); err != nil {
		_, cleanupErr := removeOwned(stage, identity, false)
		if cleanupErr == nil {
			cleanupErr = b.abortAndPrune(entry.ID)
		}
		if cleanupErr != nil {
			return cleanupRefusal("the staged link could not be moved", err, cleanupErr)
		}
		if errors.Is(err, os.ErrExist) {
			return refuseLend(wire.LendOutcomeLinkRefused, "an unrecorded file occupies the destination")
		}
		return refuseLend(wire.LendOutcomeUnavailable, "the staged link could not be moved")
	}
	if err := b.ledger.commit(entry.ID); err != nil {
		_, cleanupErr := removeOwned(link, identity, false)
		if cleanupErr == nil {
			cleanupErr = b.abortAndPrune(entry.ID)
		}
		return cleanupRefusal("the ledger could not be written", err, cleanupErr)
	}
	return wire.LendResult{Outcome: wire.LendOutcomeLent, Lend: &entry}
}

func (b *Bridge) ensureDirectory(intentID, path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return errors.New("not a directory")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := b.ensureDirectory(intentID, filepath.Dir(path)); err != nil {
		return err
	}
	if err := b.ledger.markCreating(intentID, path); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o755); errors.Is(err, os.ErrExist) {
		if clearErr := b.ledger.clearCreating(intentID, path); clearErr != nil {
			return errors.Join(errDirectoryCleanupUncertain, clearErr)
		}
		return b.ensureDirectory(intentID, path)
	} else if err != nil {
		return errors.Join(err, errDirectoryCleanupUncertain)
	}
	identity, err := identityOf(path)
	if err != nil {
		return errors.Join(err, errDirectoryCleanupUncertain)
	}
	if err := b.ledger.trackDir(intentID, path, identity); err != nil {
		_, cleanupErr := removeOwned(path, identity, true)
		if cleanupErr != nil {
			return errors.Join(err, cleanupErr, errDirectoryCleanupUncertain)
		}
		if clearErr := b.ledger.clearCreating(intentID, path); clearErr != nil {
			return errors.Join(err, clearErr, errDirectoryCleanupUncertain)
		}
		return err
	}
	return nil
}

// lentAs is the object name the ledger records: the manifest id where the
// source published one, otherwise the locator.
func lentAs(o Object) string {
	if o.ID != "" {
		return o.ID
	}
	return o.Locator
}

// Unlend removes exactly the link this entry created and the directories this
// provider made for it. The source file is untouched. The engine's own list
// may keep naming the model until that engine's idle-unload timer fires.
func (b *Bridge) Unlend(id string) wire.UnlendResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	if id == "" || len(id) > 256 {
		return wire.UnlendResult{Outcome: wire.UnlendOutcomeInvalid, Detail: "an entry id is required"}
	}
	entry, found := b.ledger.Find(id)
	if !found {
		return wire.UnlendResult{Outcome: wire.UnlendOutcomeUnknown, Detail: "no lend of that id"}
	}
	ownership, owned := b.ledger.ownedFile(id)
	if !owned {
		return wire.UnlendResult{Outcome: wire.UnlendOutcomeUnavailable, Detail: "this legacy entry has no durable ownership record"}
	}
	if err := checkParent(entry.Link, ownership.Parent); err != nil {
		return wire.UnlendResult{Outcome: wire.UnlendOutcomeUnavailable, Detail: err.Error()}
	}
	if _, err := removeOwned(entry.Link, ownership.File, false); err != nil {
		return wire.UnlendResult{Outcome: wire.UnlendOutcomeUnavailable, Detail: err.Error()}
	}
	if _, err := b.ledger.Remove(id); err != nil {
		return wire.UnlendResult{Outcome: wire.UnlendOutcomeUnavailable, Detail: "the ledger could not be written"}
	}
	if err := b.ledger.pruneOwnedDirs(); err != nil {
		return wire.UnlendResult{Outcome: wire.UnlendOutcomeUnavailable, Detail: err.Error()}
	}
	return wire.UnlendResult{Outcome: wire.UnlendOutcomeUnlent}
}

// Lends is the ledger, oldest first.
func (b *Bridge) Lends(continuation string, limit int64) wire.LendPage {
	b.mu.Lock()
	defer b.mu.Unlock()
	refuse := func(outcome wire.LendsOutcome) wire.LendPage {
		return wire.LendPage{Outcome: outcome, Lends: []wire.Lend{}}
	}
	if limit < 1 || limit > MaxPage {
		return refuse(wire.LendsOutcomeInvalid)
	}
	all := b.ledger.All()
	from := 0
	if continuation != "" {
		raw, err := base64.RawURLEncoding.DecodeString(continuation)
		if err != nil {
			return refuse(wire.LendsOutcomeInvalid)
		}
		from, err = strconv.Atoi(string(raw))
		if err != nil || from < 0 || from > len(all) {
			return refuse(wire.LendsOutcomeInvalid)
		}
	}
	to := min(from+int(limit), len(all))
	page := wire.LendPage{Outcome: wire.LendsOutcomePage, Lends: slices.Clone(all[from:to]), Complete: to == len(all)}
	if page.Lends == nil {
		page.Lends = []wire.Lend{}
	}
	if !page.Complete {
		page.Continuation = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(to)))
	}
	return page
}
