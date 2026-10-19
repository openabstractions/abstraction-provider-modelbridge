package bridge_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

var everyKind = bridge.Accepts{Kinds: []bridge.BindingKind{bridge.BindRegion, bridge.BindFile, bridge.BindCompressed, bridge.BindMemory}}

// Resolve binds only an object in a model format (MODELBRIDGE-L21). A Hugging
// Face repository holding only checkpoint.pkl and config.json is published as
// a snapshot manifest whose entries carry no weight role; it resolves to no
// binding and is not lent.
// A repository of configuration files alone is the same case with no pickle
// in it, so the refusal comes from the weight roles and the model-format
// check, never from the pickle guard.
func TestASnapshotManifestWithNoWeightsIsNotBound(t *testing.T) {
	home := t.TempDir()
	rev := "0123456789abcdef0123456789abcdef01234567"
	hub := filepath.Join(home, ".cache", "huggingface", "hub")
	repos := map[string]map[string]string{
		"Pickled": {"checkpoint.pkl": "\x80\x02pickled", "config.json": "{}"},
		"Configs": {"config.json": "{}", "tokenizer.json": "{}"},
	}
	for repo, files := range repos {
		snap := filepath.Join(hub, "models--acme--"+repo, "snapshots", rev)
		if err := os.MkdirAll(snap, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(snap, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	env := source.Env{GOOS: runtime.GOOS, Home: home, Getenv: func(string) string { return "" }}
	src := bridge.NewLocalStores(env)
	base := t.TempDir()
	eng := engine{name: "lmstudio", root: filepath.Join(base, "engine", bridge.LendDirectory)}
	b := bridge.New(src, open(t, filepath.Join(base, "state", "lends.json")), bridge.OSLinker(), eng)
	for repo := range repos {
		id := "acme/" + repo + "@" + rev[:12]
		for _, accept := range []bridge.Accepts{bridge.FileOnly, everyKind} {
			object, err := src.Resolve("huggingface", id, accept)
			if !errors.Is(err, bridge.ErrUnknownObject) && !errors.Is(err, bridge.ErrPickle) {
				t.Fatalf("%s resolved: %+v %v", id, object.Binding, err)
			}
		}
		if got := b.Lend("huggingface", id, "lmstudio", false); got.Outcome == wire.LendOutcomeLent {
			t.Fatalf("%s was lent: %+v", id, got)
		}
	}
}

// strayStore reports one stray object at locator in its store, the way an
// Ollama manifest names a blob by digest with no extension.
type strayStore struct {
	store   content.Store
	locator string
}

func (a strayStore) Program() string { return "ollama" }
func (a strayStore) Discover(source.Env) source.Discovery {
	return source.Discovery{Stores: []content.Store{a.store}}
}
func (a strayStore) Observe(s content.Store, _ source.Env) source.StoreObservation {
	return source.StoreObservation{Store: s, Objects: []content.Object{{Locator: a.locator, Size: 4096}}}
}

// An object whose path is no regular file (a directory here; a FIFO in the
// Unix test beside this one) is in no model format: ErrUnknownObject, read
// without blocking (MODELBRIDGE-L21).
func TestANonRegularObjectIsNotBound(t *testing.T) {
	root := t.TempDir()
	locator := "blobs/sha256-" + strings.Repeat("c", 64)
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(locator)), 0o755); err != nil {
		t.Fatal(err)
	}
	src := &bridge.LocalStores{Adapters: []source.Adapter{strayStore{
		store:   content.Store{Name: "ollama", Program: "ollama", Locator: root, Present: true},
		locator: locator}}}
	if _, err := src.Resolve("ollama", locator, everyKind); !errors.Is(err, bridge.ErrUnknownObject) {
		t.Fatalf("a directory resolved: %v", err)
	}
}

// A STORED nested zip that is a torch.save checkpoint is a member in no model
// format: no caller receives a region of it.
func TestANestedTorchZipIsNotBound(t *testing.T) {
	src, root, _ := packages(t)
	var inner bytes.Buffer
	w := zip.NewWriter(&inner)
	fw, err := w.CreateHeader(&zip.FileHeader{Name: "archive/data.pkl", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("\x80\x02pickled")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	writeZip(t, filepath.Join(root, "edge.zip"), member{name: "torch.zip", data: inner.Bytes(), align: 64})
	object, err := src.Resolve("packages", "edge.zip!/torch.zip", everyKind)
	if !errors.Is(err, bridge.ErrUnknownObject) && !errors.Is(err, bridge.ErrPickle) {
		t.Fatalf("the torch.save zip resolved: %+v %v", object.Binding, err)
	}
}

// A compressed pickle member is refused to a caller that accepts compressed
// bindings, and a GGUF .bin member is refused too: a container member is
// judged by its name alone (LS-9), and a .bin name is a pickle's.
func TestPickleMembersAreRefusedForEveryKind(t *testing.T) {
	src, root, _ := packages(t)
	writeZip(t, filepath.Join(root, "mixed.zip"),
		member{name: "enc.pth", data: bytes.Repeat([]byte("pickle "), 200), deflate: true},
		member{name: "ggml.bin", data: []byte("GGUF\x03\x00\x00\x00 weights"), align: 64})
	for _, id := range []string{"mixed.zip!/enc.pth", "mixed.zip!/ggml.bin"} {
		if _, err := src.Resolve("packages", id, everyKind); !errors.Is(err, bridge.ErrPickle) {
			t.Fatalf("%s: %v", id, err)
		}
	}
}
