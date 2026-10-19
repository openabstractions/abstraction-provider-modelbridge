package bridge_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/extra"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// Review of claude/setup-adopt-1031, B1: a host or lend binds a model by
// its manifest id. LS-13's on-demand hash must never rename it: Resolve
// answers the pre-hash id after the hash ends, and again after the file's
// stamp changes and its recorded digest no longer applies.
func TestResolveKeepsAPreHashIDAcrossHashAndStampChange(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "models")
	file := filepath.Join(root, "pinned-Q4_K_M.gguf")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("pinned weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := source.Env{GOOS: "test", Home: home, Getenv: func(string) string { return "" },
		Config: source.Config{ExtraRoots: []source.ExtraRoot{{Store: "mine", Path: root}}}, Digests: source.NewDigests("")}
	adapters := []source.Adapter{extra.New()}
	src := &bridge.LocalStores{Env: env, Adapters: adapters}
	idOf := func() (string, string) {
		for _, m := range snapshot.Take(env, adapters).Page.Manifests {
			if m.Manifest.Store == "mine" {
				return m.Manifest.ID, m.Manifest.Entries[0].Digest
			}
		}
		return "", ""
	}
	id, digest := idOf()
	if id == "" || digest != "" {
		t.Fatalf("before the hash: id %q digest %q", id, digest)
	}
	if object, ok := resolve(src, "mine", id); !ok || object.Path != file {
		t.Fatalf("pre-hash resolve = %+v, %v", object, ok)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Digests.Request(file, info.Size(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	env.Digests.Wait()
	if after, digest := idOf(); after != id || digest == "" {
		t.Fatalf("after the hash: id %q (was %q) digest %q", after, id, digest)
	}
	if _, ok := resolve(src, "mine", id); !ok {
		t.Fatal("the pre-hash id no longer resolves after the hash")
	}
	later := info.ModTime().Add(time.Hour)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if after, digest := idOf(); after != id || digest != "" {
		t.Fatalf("after a stamp change: id %q (was %q) digest %q", after, id, digest)
	}
	if _, ok := resolve(src, "mine", id); !ok {
		t.Fatal("the pre-hash id no longer resolves after a stamp change")
	}
}
