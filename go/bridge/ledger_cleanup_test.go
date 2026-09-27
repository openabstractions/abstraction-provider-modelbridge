package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

func TestRecoverPendingRetainsForeignStageBesidePlacedLink(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "model.gguf")
	stage := filepath.Join(base, "stage")
	if err := os.WriteFile(link, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := identityOf(link)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := identityOf(base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "state", "ledger.json")
	l := &Ledger{path: path, owned: map[string]ownedLink{}, dirs: map[string]fileID{},
		pending: []lendIntent{{Entry: wire.Lend{ID: "lend-one", Link: link, Kind: wire.LinkCopy}, Stage: stage, File: id, Parent: parent}}}
	if err := l.write(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLedger(path); err == nil {
		t.Fatal("foreign stage was accepted while pending intent was committed")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record ledgerFile
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Pending) != 1 || len(record.Lends) != 0 {
		t.Fatalf("lost durable reconciliation intent: %+v", record)
	}
}

func TestRecoverPendingRetainsIntentWhenHardlinkStageCleanupFails(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "model.gguf")
	stage := filepath.Join(base, "stage")
	if err := os.WriteFile(link, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(link, stage); err != nil {
		t.Fatal(err)
	}
	id, err := identityOf(link)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := identityOf(base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "state", "ledger.json")
	l := &Ledger{path: path, owned: map[string]ownedLink{}, dirs: map[string]fileID{},
		pending: []lendIntent{{Entry: wire.Lend{ID: "lend-one", Link: link, Kind: wire.LinkHard}, Stage: stage, File: id, Parent: parent}}}
	if err := l.write(); err != nil {
		t.Fatal(err)
	}
	q := quarantineDir(stage, id)
	if err := os.Mkdir(q, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(q, "foreign"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.recoverPending(); err == nil {
		t.Fatal("hardlink stage cleanup failure was ignored")
	}
	if len(l.pending) != 1 || len(l.lends) != 0 {
		t.Fatalf("forgot reconciliation intent: pending=%d lends=%d", len(l.pending), len(l.lends))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record ledgerFile
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Pending) != 1 || len(record.Lends) != 0 {
		t.Fatalf("lost durable reconciliation intent: %+v", record)
	}
}

func TestPruneOwnedDirsKeepsRecordAfterLedgerWriteFailure(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "owned")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := identityOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(base, "state")
	l := &Ledger{path: filepath.Join(state, "ledger.json"), dirs: map[string]fileID{dir: id}}
	if err := l.write(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(state, state+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.pruneOwnedDirs(); err == nil {
		t.Fatal("failed durable ledger write was ignored")
	}
	if _, ok := l.dirs[dir]; !ok {
		t.Fatal("forgot owned directory after failed durable ledger write")
	}
}

func TestPruneOwnedDirsReportsQuarantineCleanupFailure(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "owned")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := identityOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := &Ledger{path: filepath.Join(base, "state", "ledger.json"), dirs: map[string]fileID{dir: id}}
	if err := l.write(); err != nil {
		t.Fatal(err)
	}
	q := quarantineDir(dir, id)
	if err := os.Mkdir(q, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(q, "foreign"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.pruneOwnedDirs(); err == nil {
		t.Fatal("owned-directory quarantine cleanup failure was ignored")
	}
	if _, ok := l.dirs[dir]; !ok {
		t.Fatal("lost owned-directory record after cleanup failure")
	}
}

func TestUnlendReportsOwnedDirectoryCleanupFailure(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "owned")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(link, []byte("lent copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkID, err := identityOf(link)
	if err != nil {
		t.Fatal(err)
	}
	dirID, err := identityOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := &Ledger{path: filepath.Join(base, "state", "ledger.json"),
		lends: []wire.Lend{{ID: "lend-one", Link: link, Kind: wire.LinkCopy}},
		owned: map[string]ownedLink{"lend-one": {File: linkID, Parent: dirID}},
		dirs:  map[string]fileID{dir: dirID}}
	if err := l.write(); err != nil {
		t.Fatal(err)
	}
	q := quarantineDir(dir, dirID)
	if err := os.Mkdir(q, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(q, "foreign"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result := New(nil, l, OSLinker()).Unlend("lend-one")
	if result.Outcome != wire.UnlendOutcomeUnavailable {
		t.Fatalf("directory cleanup failure reported as successful unlend: %+v", result)
	}
	if _, ok := l.dirs[dir]; !ok {
		t.Fatal("lost owned-directory record after failed unlend cleanup")
	}
}

func TestRestartRetainsDirectoryCreationIntentUntilPathReconciled(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "engine", "owned")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "state", "ledger.json")
	l := &Ledger{path: path, owned: map[string]ownedLink{}, dirs: map[string]fileID{},
		pending: []lendIntent{{Entry: wire.Lend{ID: "lend-one", Link: filepath.Join(dir, "model.gguf"), Kind: wire.LinkSymbolic},
			Stage: filepath.Join(dir, "stage")}}}
	if err := l.write(); err != nil {
		t.Fatal(err)
	}
	if err := l.markCreating("lend-one", dir); err != nil {
		t.Fatal(err)
	}
	marked, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var beforeCreation ledgerFile
	if err := json.Unmarshal(marked, &beforeCreation); err != nil {
		t.Fatal(err)
	}
	if len(beforeCreation.Pending) != 1 || beforeCreation.Pending[0].Creating != dir {
		t.Fatalf("directory creation marker was not durable before mkdir: %+v", beforeCreation)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory existed before its durable creation marker: %v", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := identityOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foreign"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Dir(path)
	if err := os.Rename(state, state+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.trackDir("lend-one", dir, id); err == nil {
		t.Fatal("directory tracking unexpectedly survived a blocked ledger write")
	}
	if _, err := removeOwned(dir, id, true); err == nil {
		t.Fatal("nonempty created directory unexpectedly cleaned up")
	}
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(state+"-saved", state); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLedger(path); err == nil {
		t.Fatal("startup discarded a pending directory creation beside a surviving path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var durable ledgerFile
	if err := json.Unmarshal(raw, &durable); err != nil {
		t.Fatal(err)
	}
	if len(durable.Pending) != 1 || durable.Pending[0].Creating != dir {
		t.Fatalf("lost durable pending intent: %+v", durable)
	}
	if err := os.Remove(filepath.Join(dir, "foreign")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	q := quarantineDir(dir, id)
	if err := os.Mkdir(q, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignQuarantineFile := filepath.Join(q, "foreign")
	if err := os.WriteFile(foreignQuarantineFile, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLedger(path); err == nil {
		t.Fatal("startup discarded a creation intent with an unresolved undo quarantine")
	}
	if got, err := os.ReadFile(foreignQuarantineFile); err != nil || string(got) != "replacement" {
		t.Fatalf("changed unresolved quarantine: %q, %v", got, err)
	}
	if err := os.Remove(foreignQuarantineFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(q); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLedger(path); err != nil {
		t.Fatalf("manual path reconciliation did not release the intent: %v", err)
	}
}

func TestFailedDirectoryMarkerClearKeepsPendingState(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "state", "ledger.json")
	dir := filepath.Join(base, "engine", "owned")
	l := &Ledger{path: path, owned: map[string]ownedLink{}, dirs: map[string]fileID{},
		pending: []lendIntent{{Entry: wire.Lend{ID: "lend-one", Link: filepath.Join(dir, "model.gguf"), Kind: wire.LinkSymbolic}}}}
	if err := l.write(); err != nil {
		t.Fatal(err)
	}
	if err := l.markCreating("lend-one", dir); err != nil {
		t.Fatal(err)
	}
	state := filepath.Dir(path)
	if err := os.Rename(state, state+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.clearCreating("lend-one", dir); err == nil {
		t.Fatal("marker clear unexpectedly survived a blocked ledger write")
	}
	if len(l.pending) != 1 || l.pending[0].Creating != dir {
		t.Fatalf("forgot pending directory marker after failed clear: %+v", l.pending)
	}
}

func TestPreexistingDirectoryIsPreservedWithoutCreationMarker(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "engine", "owned")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "foreign")
	if err := os.WriteFile(foreign, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &Ledger{path: filepath.Join(base, "state", "ledger.json"), owned: map[string]ownedLink{}, dirs: map[string]fileID{},
		pending: []lendIntent{{Entry: wire.Lend{ID: "lend-one", Link: filepath.Join(dir, "model.gguf"), Kind: wire.LinkSymbolic}}}}
	if err := l.write(); err != nil {
		t.Fatal(err)
	}
	if err := New(nil, l, OSLinker()).ensureDirectory("lend-one", dir); err != nil {
		t.Fatal(err)
	}
	if l.pending[0].Creating != "" || len(l.dirs) != 0 {
		t.Fatalf("claimed preexisting directory: pending=%+v dirs=%+v", l.pending, l.dirs)
	}
	if got, err := os.ReadFile(foreign); err != nil || string(got) != "replacement" {
		t.Fatalf("changed preexisting directory content: %q, %v", got, err)
	}
}
