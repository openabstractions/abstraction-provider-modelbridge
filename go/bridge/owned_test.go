package bridge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

func TestFinishQuarantineReportsCleanupFailureAndRemovalState(t *testing.T) {
	q := filepath.Join(t.TempDir(), "quarantine")
	if err := os.Mkdir(q, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(q, "unexpected"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	primary := errors.New("move failed")
	for _, tc := range []struct {
		removed bool
		primary error
	}{
		{removed: false, primary: primary},
		{removed: true, primary: nil},
	} {
		gotRemoved, err := finishQuarantine(q, tc.removed, tc.primary)
		if gotRemoved != tc.removed || err == nil || (tc.primary != nil && !errors.Is(err, tc.primary)) || !strings.Contains(err.Error(), "remove quarantine "+q) {
			t.Fatalf("removed=%v: got (%v, %v)", tc.removed, gotRemoved, err)
		}
	}
	if err := os.Remove(filepath.Join(q, "unexpected")); err != nil {
		t.Fatal(err)
	}
	if removed, err := finishQuarantine(q, true, nil); !removed || err != nil {
		t.Fatalf("empty quarantine: removed=%v, err=%v", removed, err)
	}
}

func TestFailedCopyPreservesPrimaryAndCleanupError(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	other := filepath.Join(base, "other")
	if err := os.WriteFile(target, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := os.Lstat(other)
	if err != nil {
		t.Fatal(err)
	}
	primary := errors.New("copy failed")
	err = failedCopy(target, created, primary)
	if !errors.Is(err, primary) || !strings.Contains(err.Error(), "incomplete copy was replaced") {
		t.Fatalf("lost copy or cleanup error: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "replacement" {
		t.Fatalf("replacement was not restored: %q, %v", data, err)
	}
}

func TestDirectoryRestorationPreservesReservedDestination(t *testing.T) {
	base := t.TempDir()
	from, to := filepath.Join(base, "quarantined"), filepath.Join(base, "replacement")
	for _, path := range []string{from, to} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	source, err := identityOf(from)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := identityOf(to)
	if err != nil {
		t.Fatal(err)
	}
	if err := moveDirectoryNoReplace(from, to); !errors.Is(err, os.ErrExist) {
		t.Fatalf("exclusive directory move: %v", err)
	}
	if got, err := identityOf(from); err != nil || got != source {
		t.Fatalf("quarantined directory changed: %v, %v", got, err)
	}
	if got, err := identityOf(to); err != nil || got != replacement {
		t.Fatalf("replacement directory changed: %v, %v", got, err)
	}
	if err := os.Remove(to); err != nil {
		t.Fatal(err)
	}
	if err := moveBack(from, to, true); err != nil {
		t.Fatal(err)
	}
	if got, err := identityOf(to); err != nil || got != source {
		t.Fatalf("restored directory identity: %v, %v", got, err)
	}
}

func TestRestartFinishesAnInterruptedUndo(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "model.gguf")
	if err := os.WriteFile(link, []byte("lent copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity, err := identityOf(link)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(base, "state", "lends.json")
	l, err := OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	l.lends = append(l.lends, wire.Lend{ID: "lend-test", Engine: "engine", Store: "store", Object: "object", Name: "model", Link: link, Kind: wire.LinkCopy, Created: "now"})
	parent, err := identityOf(base)
	if err != nil {
		t.Fatal(err)
	}
	l.owned["lend-test"] = ownedLink{File: identity, Parent: parent}
	if err := l.write(); err != nil {
		t.Fatal(err)
	}
	q := quarantineDir(link, identity)
	if err := os.Mkdir(q, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(link, filepath.Join(q, filepath.Base(link))); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(q); !os.IsNotExist(err) {
		t.Fatalf("owned quarantine survived restart: %v", err)
	}
	if got := New(nil, reopened, OSLinker()).Unlend("lend-test"); got.Outcome != wire.UnlendOutcomeUnlent {
		t.Fatalf("unlend after recovery: %+v", got)
	}
}

func TestRestartRestoresAQuarantinedForeignReplacement(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "model.gguf")
	if err := os.WriteFile(link, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity, err := identityOf(link)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(base, "state", "lends.json")
	l, err := OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	l.lends = append(l.lends, wire.Lend{ID: "lend-test", Engine: "engine", Store: "store", Object: "object", Name: "model", Link: link, Kind: wire.LinkCopy, Created: "now"})
	parent, err := identityOf(base)
	if err != nil {
		t.Fatal(err)
	}
	l.owned["lend-test"] = ownedLink{File: identity, Parent: parent}
	if err := l.write(); err != nil {
		t.Fatal(err)
	}
	q := quarantineDir(link, identity)
	if err := os.Mkdir(q, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(q, filepath.Base(link)), []byte("foreign replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLedger(ledgerPath); err == nil {
		t.Fatal("a foreign replacement was accepted as an owned undo")
	}
	if data, err := os.ReadFile(link); err != nil || string(data) != "foreign replacement" {
		t.Fatalf("replacement was not restored: %v %q", err, data)
	}
}
