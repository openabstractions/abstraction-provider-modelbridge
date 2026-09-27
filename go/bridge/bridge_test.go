package bridge_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

// store is a source of exactly what a test placed on disk.
type store struct {
	name string
	root string
	// objects maps the id a caller names to the store-relative locator.
	objects map[string]string
}

func (s store) Stores() []string { return []string{s.name} }

func (s store) Root(name string) string {
	if name == s.name {
		return s.root
	}
	return ""
}

func (s store) Resolve(name, id string) (bridge.Object, bool) {
	locator, ok := s.objects[id]
	if name != s.name || !ok {
		return bridge.Object{}, false
	}
	path := filepath.Join(s.root, filepath.FromSlash(locator))
	info, err := os.Stat(path)
	if err != nil {
		return bridge.Object{}, false
	}
	base := filepath.Base(locator)
	return bridge.Object{Store: name, ID: id, Locator: locator, Path: path,
		Family: bridge.Family(base), File: base, Size: info.Size(), Kind: bridge.KindOf(name, locator)}, true
}

// engine is a directory standing in for an installed engine's own models
// folder. told counts how often the engine had to be pointed at it.
type engine struct {
	name string
	root string
	told *int
	fail error
}

func (e engine) Name() string { return e.name }

func (e engine) Root(bridge.Object) (string, error) {
	if e.fail != nil {
		return "", e.fail
	}
	return e.root, nil
}

func (e engine) Accept(string) error {
	if e.told != nil {
		*e.told++
	}
	return e.fail
}

// machine lays out one source file, one engine directory and a ledger.
func machine(t *testing.T) (store, engine, string) {
	t.Helper()
	base := t.TempDir()
	source := filepath.Join(base, "store")
	if err := os.MkdirAll(filepath.Join(source, "snapshots", "abc"), 0o755); err != nil {
		t.Fatal(err)
	}
	locator := "snapshots/abc/SmolLM2-135M-Instruct-Q2_K.gguf"
	if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(locator)), []byte("GGUF weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "engine", "models", bridge.LendDirectory)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	return store{name: "huggingface", root: source, objects: map[string]string{"manifest-1": locator}},
		engine{name: "lmstudio", root: root}, filepath.Join(base, "state", "lends.json")
}

func open(t *testing.T, path string) *bridge.Ledger {
	t.Helper()
	ledger, err := bridge.OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestUnrecordedDestinationIsRefusedWithoutInventingALend(t *testing.T) {
	for _, duringPlacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "racing"}[duringPlacement], func(t *testing.T) {
			src, eng, ledgerPath := machine(t)
			object, _ := src.Resolve("huggingface", "manifest-1")
			destination := filepath.Join(eng.root, object.Family, object.File)
			plant := func() {
				if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(destination, []byte("foreign file"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			linker := bridge.OSLinker()
			if duringPlacement {
				linker.SymlinksRefused = true
				linker.Hardlink = func(from, to string) error { plant(); return os.Link(from, to) }
			} else {
				plant()
			}
			ledger := open(t, ledgerPath)
			b := bridge.New(src, ledger, linker, eng)
			got := b.Lend("huggingface", "manifest-1", "lmstudio", false)
			if got.Outcome != wire.LendOutcomeLinkRefused || got.Lend != nil {
				t.Fatalf("occupied destination: %+v, want link_refused without a lend", got)
			}
			if data, err := os.ReadFile(destination); err != nil || string(data) != "foreign file" {
				t.Fatalf("foreign file changed: %q, %v", data, err)
			}
			if len(ledger.All()) != 0 {
				t.Fatal("foreign file adopted into ledger")
			}
		})
	}
}

// A machine that refuses this process a symbolic link still lends: the hard
// link is the fallback, exactly as the 2026-09-22 experiment measured on
// Windows without the privilege.
func TestASymbolicLinkTheSystemRefusesFallsBackToAHardLink(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	told := 0
	eng.told = &told
	linker := bridge.OSLinker()
	linker.Symlink = func(string, string) error { return errors.New("a required privilege is not held by the client") }
	b := bridge.New(src, open(t, ledgerPath), linker, eng)

	result := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if result.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lend %s: %s", result.Outcome, result.Detail)
	}
	if result.Lend.Kind != wire.LinkHard {
		t.Fatalf("link kind %s, wanted hard", result.Lend.Kind)
	}
	if told != 1 {
		t.Fatalf("the engine was pointed at its directory %d times, wanted once", told)
	}
	if result.Lend.Name != "smollm2-135m-instruct" {
		t.Fatalf("directory name %q; LM Studio derives its model name from it", result.Lend.Name)
	}
	want := filepath.Join(eng.root, "smollm2-135m-instruct", "SmolLM2-135M-Instruct-Q2_K.gguf")
	if result.Lend.Link != want {
		t.Fatalf("link %s, wanted %s", result.Lend.Link, want)
	}
	linked, err := os.ReadFile(want)
	if err != nil || string(linked) != "GGUF weights" {
		t.Fatalf("the engine cannot read the lent file: %v %q", err, linked)
	}
	// The original is where it was, byte for byte.
	original, err := os.ReadFile(filepath.Join(src.root, "snapshots", "abc", "SmolLM2-135M-Instruct-Q2_K.gguf"))
	if err != nil || string(original) != "GGUF weights" {
		t.Fatalf("the source moved or changed: %v %q", err, original)
	}
}

// No link of either kind, and no copy asked for, is link_refused. The engine's
// directory is left as it was found.
func TestALinkNeitherKindAllowsIsRefusedAndLeavesNothingBehind(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	linker := bridge.Linker{
		Symlink:    func(string, string) error { return errors.New("privilege not held") },
		Hardlink:   func(string, string) error { return errors.New("hard link never reached") },
		SameVolume: func(string, string) (bool, error) { return false, nil },
	}
	b := bridge.New(src, open(t, ledgerPath), linker, eng)

	result := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if result.Outcome != wire.LendOutcomeLinkRefused {
		t.Fatalf("lend %s: %s", result.Outcome, result.Detail)
	}
	if _, err := os.Stat(eng.root); !os.IsNotExist(err) {
		t.Fatalf("the refused lend left %s behind: %v", eng.root, err)
	}
	if page := b.Lends("", 10); len(page.Lends) != 0 {
		t.Fatalf("a refused lend was recorded: %+v", page.Lends)
	}
}

// A caller that asks for a copy gets one across a volume boundary, counted and
// listed as a copy.
func TestACopyIsMadeOnlyWhenItIsAskedFor(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	linker := bridge.Linker{
		Symlink:    func(string, string) error { return errors.New("privilege not held") },
		SameVolume: func(string, string) (bool, error) { return false, nil },
	}
	b := bridge.New(src, open(t, ledgerPath), linker, eng)

	result := b.Lend("huggingface", "manifest-1", "lmstudio", true)
	if result.Outcome != wire.LendOutcomeLent || result.Lend.Kind != wire.LinkCopy {
		t.Fatalf("lend %s kind %v: %s", result.Outcome, result.Lend, result.Detail)
	}
}

// Unlend removes exactly the link and the directories the lend created, and
// leaves the source and the engine's own folder alone.
func TestUnlendRemovesTheLinkAndNothingElse(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	b := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	// A file the engine already had, which no lend may touch.
	own := filepath.Join(filepath.Dir(eng.root), "publisher", "own.gguf")
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte("the engine's own"), 0o644); err != nil {
		t.Fatal(err)
	}

	lent := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if lent.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lend %s: %s", lent.Outcome, lent.Detail)
	}
	unlent := b.Unlend(lent.Lend.ID)
	if unlent.Outcome != wire.UnlendOutcomeUnlent {
		t.Fatalf("unlend %s: %s", unlent.Outcome, unlent.Detail)
	}
	if _, err := os.Lstat(lent.Lend.Link); !os.IsNotExist(err) {
		t.Fatalf("the link is still there: %v", err)
	}
	if _, err := os.Stat(eng.root); !os.IsNotExist(err) {
		t.Fatalf("the lend directory %s was not pruned: %v", eng.root, err)
	}
	if kept, err := os.ReadFile(own); err != nil || string(kept) != "the engine's own" {
		t.Fatalf("the engine's own file was touched: %v %q", err, kept)
	}
	if kept, err := os.ReadFile(filepath.Join(src.root, "snapshots", "abc", "SmolLM2-135M-Instruct-Q2_K.gguf")); err != nil || string(kept) != "GGUF weights" {
		t.Fatalf("the source was touched: %v %q", err, kept)
	}
	if page := b.Lends("", 10); page.Outcome != wire.LendsOutcomePage || len(page.Lends) != 0 {
		t.Fatalf("the entry stayed in the ledger: %+v", page)
	}
	if second := b.Unlend(lent.Lend.ID); second.Outcome != wire.UnlendOutcomeUnknown {
		t.Fatalf("a second unlend read %s, wanted unknown", second.Outcome)
	}
}

func TestUnlendKeepsAReplacementFile(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	b := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	lent := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if lent.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lend: %s: %s", lent.Outcome, lent.Detail)
	}
	if err := os.Remove(lent.Lend.Link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lent.Lend.Link, []byte("owner's replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	b = bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	if got := b.Unlend(lent.Lend.ID); got.Outcome != wire.UnlendOutcomeUnavailable {
		t.Fatalf("unlend of replacement: %s, wanted unavailable", got.Outcome)
	}
	if data, err := os.ReadFile(lent.Lend.Link); err != nil || string(data) != "owner's replacement" {
		t.Fatalf("replacement changed: %v %q", err, data)
	}
	if page := b.Lends("", 10); len(page.Lends) != 1 {
		t.Fatalf("lost the entry needed for reconciliation: %+v", page)
	}
}

// A crash between moving the staged link into place and committing the row
// leaves a durable intent. Opening the ledger finishes that row, including its
// concrete record, rather than answering already_lent without one.
func TestRestartCompletesAPlacedIntent(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	first := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	lent := first.Lend("huggingface", "manifest-1", "lmstudio", false)
	if lent.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lend: %s: %s", lent.Outcome, lent.Detail)
	}
	stage := filepath.Join(filepath.Dir(lent.Lend.Link), ".openabstractions-stage-"+lent.Lend.ID)
	recordAsPending(t, ledgerPath, *lent.Lend, stage)
	second := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	if got := second.Lend("huggingface", "manifest-1", "lmstudio", false); got.Outcome != wire.LendOutcomeAlreadyLent || got.Lend == nil || got.Lend.ID != lent.Lend.ID {
		t.Fatalf("recovered lend: %+v", got)
	}
	if page := second.Lends("", 10); len(page.Lends) != 1 {
		t.Fatalf("recovered ledger: %+v", page)
	}
}

func TestRestartReversesAStagedIntent(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	first := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	lent := first.Lend("huggingface", "manifest-1", "lmstudio", false)
	if lent.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lend: %s: %s", lent.Outcome, lent.Detail)
	}
	stage := filepath.Join(filepath.Dir(lent.Lend.Link), ".openabstractions-stage-"+lent.Lend.ID)
	if err := os.Rename(lent.Lend.Link, stage); err != nil {
		t.Fatal(err)
	}
	recordAsPending(t, ledgerPath, *lent.Lend, stage)
	second := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	if page := second.Lends("", 10); len(page.Lends) != 0 {
		t.Fatalf("staged lend survived recovery: %+v", page)
	}
	if _, err := os.Lstat(stage); !os.IsNotExist(err) {
		t.Fatalf("staged file survived recovery: %v", err)
	}
	if got := second.Lend("huggingface", "manifest-1", "lmstudio", false); got.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lend after recovery: %s: %s", got.Outcome, got.Detail)
	}
}

func TestAnAmbiguousStagedArtifactBlocksRetry(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	linker := bridge.Linker{SymlinksRefused: true, Hardlink: func(source, target string) error {
		if err := os.Link(source, target); err != nil {
			return err
		}
		return errors.New("process stopped before placement was acknowledged")
	}}
	b := bridge.New(src, open(t, ledgerPath), linker, eng)
	first := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if first.Outcome != wire.LendOutcomeUnavailable {
		t.Fatalf("ambiguous placement: %+v", first)
	}
	second := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if second.Outcome != wire.LendOutcomeUnavailable {
		t.Fatalf("retry over pending intent: %+v", second)
	}
	if page := b.Lends("", 10); len(page.Lends) != 0 {
		t.Fatalf("ambiguous placement became a committed lend: %+v", page)
	}
	family := filepath.Join(eng.root, "smollm2-135m-instruct")
	items, err := os.ReadDir(family)
	if err != nil || len(items) != 1 {
		t.Fatalf("retry changed staged artifacts: %v, %d entries", err, len(items))
	}
}

func TestFailedAbortCannotReportOrdinaryLinkRefusal(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	state := filepath.Dir(ledgerPath)
	linker := bridge.Linker{SymlinksRefused: true, Hardlink: func(_, _ string) error {
		if err := os.Rename(state, state+"-saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(state, []byte("blocked"), 0o600); err != nil {
			t.Fatal(err)
		}
		return bridge.ErrLinkRefused
	}}
	b := bridge.New(src, open(t, ledgerPath), linker, eng)
	result := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if result.Outcome != wire.LendOutcomeUnavailable {
		t.Fatalf("failed abort classified as an ordinary link refusal: %+v", result)
	}
	if retry := b.Lend("huggingface", "manifest-1", "lmstudio", false); retry.Outcome != wire.LendOutcomeUnavailable {
		t.Fatalf("failed abort allowed a retry over pending intent: %+v", retry)
	}
}

func recordAsPending(t *testing.T, path string, entry wire.Lend, stage string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	var owned map[string]json.RawMessage
	if err := json.Unmarshal(record["owned"], &owned); err != nil {
		t.Fatal(err)
	}
	var ownership struct {
		File   json.RawMessage `json:"file"`
		Parent json.RawMessage `json:"parent"`
	}
	if err := json.Unmarshal(owned[entry.ID], &ownership); err != nil {
		t.Fatal(err)
	}
	delete(owned, entry.ID)
	record["owned"], err = json.Marshal(owned)
	if err != nil {
		t.Fatal(err)
	}
	record["lends"] = json.RawMessage(`[]`)
	record["pending"], err = json.Marshal([]struct {
		Entry  wire.Lend       `json:"entry"`
		Stage  string          `json:"stage"`
		File   json.RawMessage `json:"file"`
		Parent json.RawMessage `json:"parent"`
	}{{entry, stage, ownership.File, ownership.Parent}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUnlendKeepsPreexistingEmptyDirectories(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	family := filepath.Join(eng.root, "smollm2-135m-instruct")
	if err := os.MkdirAll(family, 0o755); err != nil {
		t.Fatal(err)
	}
	b := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	lent := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if lent.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lend: %s: %s", lent.Outcome, lent.Detail)
	}
	if got := b.Unlend(lent.Lend.ID); got.Outcome != wire.UnlendOutcomeUnlent {
		t.Fatalf("unlend: %s: %s", got.Outcome, got.Detail)
	}
	if info, err := os.Stat(family); err != nil || !info.IsDir() {
		t.Fatalf("preexisting family directory removed: %v", err)
	}
	if info, err := os.Stat(eng.root); err != nil || !info.IsDir() {
		t.Fatalf("preexisting lend root removed: %v", err)
	}
}

func TestLastUnlendPrunesSharedOwnedRoot(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	secondPath := filepath.Join(src.root, "snapshots", "abc", "Another-7B-Q4_K.gguf")
	if err := os.WriteFile(secondPath, []byte("another model"), 0o644); err != nil {
		t.Fatal(err)
	}
	src.objects["manifest-2"] = "snapshots/abc/Another-7B-Q4_K.gguf"
	b := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	first := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	second := b.Lend("huggingface", "manifest-2", "lmstudio", false)
	if first.Outcome != wire.LendOutcomeLent || second.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lends: %+v %+v", first, second)
	}
	if got := b.Unlend(first.Lend.ID); got.Outcome != wire.UnlendOutcomeUnlent {
		t.Fatalf("first unlend: %+v", got)
	}
	if _, err := os.Stat(eng.root); err != nil {
		t.Fatalf("shared root pruned early: %v", err)
	}
	b = bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	if got := b.Unlend(second.Lend.ID); got.Outcome != wire.UnlendOutcomeUnlent {
		t.Fatalf("second unlend: %+v", got)
	}
	if _, err := os.Stat(eng.root); !os.IsNotExist(err) {
		t.Fatalf("last unlend left the root: %v", err)
	}
}

func TestUnlendKeepsAReplacementDirectory(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	b := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	lent := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if lent.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lend: %s: %s", lent.Outcome, lent.Detail)
	}
	family := filepath.Dir(lent.Lend.Link)
	moved := family + "-moved"
	if err := os.Rename(family, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(family, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := b.Unlend(lent.Lend.ID); got.Outcome != wire.UnlendOutcomeUnavailable {
		t.Fatalf("unlend through replacement directory: %+v", got)
	}
	if _, err := os.Stat(family); err != nil {
		t.Fatalf("replacement directory removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(moved, filepath.Base(lent.Lend.Link))); err != nil {
		t.Fatalf("original lend lost its reconciliation path: %v", err)
	}
	if page := b.Lends("", 10); len(page.Lends) != 1 {
		t.Fatalf("ownership record disappeared: %+v", page)
	}
}

// decode reads a JSON request body into value.
func decode(r *http.Request, value any) error { return json.NewDecoder(r.Body).Decode(value) }

// The ledger, not the engine's list, says whether a lend is in force: a fresh
// process reads back what the last one wrote.
func TestTheLedgerSurvivesARestart(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	first := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	lent := first.Lend("huggingface", "manifest-1", "lmstudio", false)
	if lent.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lend %s: %s", lent.Outcome, lent.Detail)
	}

	second := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	page := second.Lends("", 10)
	if page.Outcome != wire.LendsOutcomePage || len(page.Lends) != 1 {
		t.Fatalf("after a restart the ledger reads %+v", page)
	}
	if page.Lends[0] != *lent.Lend {
		t.Fatalf("the entry changed across the restart:\n%+v\n%+v", page.Lends[0], *lent.Lend)
	}
	if again := second.Lend("huggingface", "manifest-1", "lmstudio", false); again.Outcome != wire.LendOutcomeAlreadyLent {
		t.Fatalf("a second lend of the same object read %s, wanted already_lent", again.Outcome)
	}
}

func TestDuplicateLendRequiresItsRecordedPlacement(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{name: "removed", change: func(t *testing.T, link string) {
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replaced", change: func(t *testing.T, link string) {
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(link, []byte("foreign replacement"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "parent replaced", change: func(t *testing.T, link string) {
			parent := filepath.Dir(link)
			if err := os.Rename(parent, parent+"-moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(link, []byte("foreign replacement"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			src, eng, ledgerPath := machine(t)
			ledger := open(t, ledgerPath)
			b := bridge.New(src, ledger, bridge.OSLinker(), eng)
			first := b.Lend("huggingface", "manifest-1", "lmstudio", false)
			if first.Outcome != wire.LendOutcomeLent {
				t.Fatalf("initial lend: %+v", first)
			}
			scenario.change(t, first.Lend.Link)
			got := b.Lend("huggingface", "manifest-1", "lmstudio", false)
			if got.Outcome != wire.LendOutcomeUnavailable || got.Lend != nil {
				t.Fatalf("duplicate over changed placement: %+v, want unavailable without lend", got)
			}
			if len(ledger.All()) != 1 {
				t.Fatal("lost ledger entry required for reconciliation")
			}
			if scenario.name != "removed" {
				data, err := os.ReadFile(first.Lend.Link)
				if err != nil || string(data) != "foreign replacement" {
					t.Fatalf("foreign replacement changed: %q, %v", data, err)
				}
			}
		})
	}
}

func TestDuplicateLendRequiresDurableOwnershipReceipt(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	first := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng).Lend("huggingface", "manifest-1", "lmstudio", false)
	if first.Outcome != wire.LendOutcomeLent {
		t.Fatalf("initial lend: %+v", first)
	}
	legacy := open(t, ledgerPath+"-legacy")
	if err := legacy.Add(*first.Lend); err != nil {
		t.Fatal(err)
	}
	got := bridge.New(src, legacy, bridge.OSLinker(), eng).Lend("huggingface", "manifest-1", "lmstudio", false)
	if got.Outcome != wire.LendOutcomeUnavailable || got.Lend != nil {
		t.Fatalf("legacy entry without ownership receipt: %+v, want unavailable", got)
	}
	if _, err := os.Lstat(first.Lend.Link); err != nil || len(legacy.All()) != 1 {
		t.Fatalf("legacy artifact or ledger changed: %v", err)
	}
}

func TestDuplicateLendReturnsActualKindDespiteCopyPreference(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	ledger := open(t, ledgerPath)
	linker := bridge.OSLinker()
	linker.SymlinksRefused = true
	b := bridge.New(src, ledger, linker, eng)
	first := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if first.Outcome != wire.LendOutcomeLent || first.Lend == nil || first.Lend.Kind != wire.LinkHard {
		t.Fatalf("initial hard-link lend: %+v", first)
	}
	got := b.Lend("huggingface", "manifest-1", "lmstudio", true)
	if got.Outcome != wire.LendOutcomeAlreadyLent || got.Lend == nil || got.Lend.ID != first.Lend.ID || got.Lend.Kind != wire.LinkHard {
		t.Fatalf("duplicate with copy requested: %+v, want existing hard-link entry", got)
	}
	if len(ledger.All()) != 1 {
		t.Fatal("duplicate lend added a ledger entry")
	}
}

// An engine nobody declared, and an object no store holds, are each their own
// refusal rather than a failure.
func TestTheRefusalsAreTyped(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	b := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	for _, c := range []struct {
		store, object, engine string
		want                  wire.LendOutcome
	}{
		{"huggingface", "manifest-1", "ollama", wire.LendOutcomeUnsupportedEngine},
		{"huggingface", "nothing-here", "lmstudio", wire.LendOutcomeUnknownObject},
		{"no-such-store", "manifest-1", "lmstudio", wire.LendOutcomeUnknownObject},
		{"", "manifest-1", "lmstudio", wire.LendOutcomeInvalid},
	} {
		if got := b.Lend(c.store, c.object, c.engine, false); got.Outcome != c.want {
			t.Errorf("lend %q %q %q read %s, wanted %s", c.store, c.object, c.engine, got.Outcome, c.want)
		}
	}
	if page := b.Lends("", 0); page.Outcome != wire.LendsOutcomeInvalid {
		t.Errorf("a limit of zero read %s", page.Outcome)
	}
	if page := b.Lends("not-a-cursor", 10); page.Outcome != wire.LendsOutcomeInvalid {
		t.Errorf("an unknown continuation read %s", page.Outcome)
	}
}

// An engine whose own directory cannot be named refuses the lend rather than
// writing somewhere else.
func TestAnEngineThatCannotBePointedAtItsDirectoryIsUnsupported(t *testing.T) {
	src, eng, ledgerPath := machine(t)
	eng.fail = errors.New("Lemonade at http://localhost:8000 did not answer")
	b := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	result := b.Lend("huggingface", "manifest-1", "lmstudio", false)
	if result.Outcome != wire.LendOutcomeUnsupportedEngine {
		t.Fatalf("lend %s: %s", result.Outcome, result.Detail)
	}
	if result.Detail == "" {
		t.Fatal("an unsupported engine carries no reason")
	}
}

// Lemonade is pointed at the lent directory through its own settings endpoint;
// a server that refuses leaves the lend unsupported_engine, because this
// provider never edits another program's configuration file.
func TestLemonadeIsPointedAtTheLentDirectoryThroughItsOwnEndpoint(t *testing.T) {
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/set" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := decode(r, &got); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	directory := filepath.Join(t.TempDir(), "lent", "lemonade")
	lemonade := bridge.Lemonade{Directory: directory, Base: server.URL}
	root, err := lemonade.Root(bridge.Object{})
	if err != nil || root != directory {
		t.Fatalf("root %q: %v", root, err)
	}
	if err := lemonade.Accept(root); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if got["extra_models_dir"] != filepath.ToSlash(directory) {
		t.Fatalf("extra_models_dir %q, wanted %q", got["extra_models_dir"], filepath.ToSlash(directory))
	}

	if _, err := (bridge.Lemonade{Directory: directory}).Root(bridge.Object{}); err == nil {
		t.Fatal("a Lemonade with no address was accepted")
	}
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer refusing.Close()
	if err := (bridge.Lemonade{Directory: directory, Base: refusing.URL}).Accept(directory); err == nil {
		t.Fatal("a Lemonade that refused the setting was accepted")
	}
}

// A store that names every weight file model.gguf — Jan does — lends under
// the name that store gives the model, not under "model".
func TestAGenericFileNameFallsBackToTheStoresOwnName(t *testing.T) {
	for _, c := range []struct{ name, file, want string }{
		{"sentence-transformer-mini", "model.gguf", "sentence-transformer-mini"},
		{"unsloth/SmolLM2-135M-Instruct-GGUF/SmolLM2-135M-Instruct-Q2_K.gguf", "SmolLM2-135M-Instruct-Q2_K.gguf", "smollm2-135m-instruct"},
		{"acme/Tiny-GGUF/model.gguf", "model.gguf", "tiny-gguf"},
		{"", "model.gguf", "model"},
	} {
		if got := bridge.FamilyOf(c.name, c.file); got != c.want {
			t.Errorf("FamilyOf(%q, %q) = %q, wanted %q", c.name, c.file, got, c.want)
		}
	}
}

func TestFamilyDropsTheQuantisationToken(t *testing.T) {
	for file, want := range map[string]string{
		"SmolLM2-135M-Instruct-Q2_K.gguf":  "smollm2-135m-instruct",
		"Qwen3-8B-Instruct-Q4_K_M.gguf":    "qwen3-8b-instruct",
		"model.bf16.safetensors":           "model",
		"llama-3.1-8b.gguf":                "llama-3.1-8b",
		"Mistral-Small-3.2-24B-IQ3_M.gguf": "mistral-small-3.2-24b",
		".gguf":                            "model",
	} {
		if got := bridge.Family(file); got != want {
			t.Errorf("Family(%q) = %q, wanted %q", file, got, want)
		}
	}
}
