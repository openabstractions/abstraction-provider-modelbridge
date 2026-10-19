package bridge_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

func TestLendsContinuationBoundToInstance(t *testing.T) {
	ledger := open(t, filepath.Join(t.TempDir(), "ledger.json"))
	for _, id := range []string{"first", "second"} {
		if err := ledger.Add(wire.Lend{ID: id, Kind: wire.LinkHard}); err != nil {
			t.Fatal(err)
		}
	}
	b := bridge.New(nil, ledger, bridge.Linker{})
	page := b.Lends("", 1)
	if page.Outcome != wire.LendsOutcomePage || page.Complete || page.Continuation == "" || page.Lends[0].ID != "first" {
		t.Fatal(page)
	}
	next := b.Lends(page.Continuation, 1)
	if next.Outcome != wire.LendsOutcomePage || !next.Complete || len(next.Lends) != 1 || next.Lends[0].ID != "second" {
		t.Fatal(next)
	}
	restarted := bridge.New(nil, ledger, bridge.Linker{})
	for _, c := range []string{page.Continuation, "MQ", "malformed", strings.Repeat("x", 257)} {
		got := restarted.Lends(c, 1)
		if got.Outcome != wire.LendsOutcomeInvalid || len(got.Lends) != 0 || got.Continuation != "" || got.Complete {
			t.Fatalf("%q: %+v", c, got)
		}
	}
	if got := restarted.Lends("", 1); got.Outcome != wire.LendsOutcomePage || got.Lends[0].ID != "first" {
		t.Fatal(got)
	}
}
