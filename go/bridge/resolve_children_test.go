package bridge_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/ollama"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

type childStore struct {
	root      string
	manifests []content.ManifestHolders
}

func TestResolveOllamaRealManifestList(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "mlx-first", true: "gguf-first"}[reverse], func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, ".ollama", "models")
			blob := func(body []byte) map[string]any {
				t.Helper()
				sum := sha256.Sum256(body)
				digest := hex.EncodeToString(sum[:])
				p := filepath.Join(root, "blobs", "sha256-"+digest)
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, body, 0600); err != nil {
					t.Fatal(err)
				}
				return map[string]any{"digest": "sha256:" + digest, "size": len(body)}
			}
			config := blob([]byte(`{}`))
			config["mediaType"] = "application/vnd.docker.container.image.v1+json"
			var children []any
			for _, runner := range []string{"mlx", "llamacpp"} {
				body, kind, format := "TENSORS!", "tensor", "safetensors"
				if runner == "llamacpp" {
					body, kind, format = "GGUFtiny", "model", "gguf"
				}
				layer := blob([]byte(body))
				layer["mediaType"] = "application/vnd.ollama.image." + kind
				children = append(children, map[string]any{"schemaVersion": 2,
					"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
					"config":    config, "layers": []any{layer}, "runner": runner, "format": format})
			}
			if reverse {
				children[0], children[1] = children[1], children[0]
			}
			body, err := json.Marshal(map[string]any{"schemaVersion": 2,
				"mediaType": "application/vnd.ollama.manifest.list.v2+json", "manifests": children})
			if err != nil {
				t.Fatal(err)
			}
			blob(body)
			p := filepath.Join(root, "manifests-v2", "ollama.com", "library", "multi", "latest")
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, body, 0600); err != nil {
				t.Fatal(err)
			}
			src := &bridge.LocalStores{Env: source.Env{GOOS: runtime.GOOS, Home: home, Getenv: func(string) string { return "" }}, Adapters: []source.Adapter{ollama.New()}}
			got, err := src.Resolve("ollama", "library/multi:latest", bridge.FileOnly)
			want := sha256.Sum256([]byte("GGUFtiny"))
			if err != nil || got.Locator != "blobs/sha256-"+hex.EncodeToString(want[:]) {
				t.Fatalf("%+v, %v", got, err)
			}
		})
	}
}

func TestResolveOllamaChildRefusalsStayTerminal(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "corrupt-container", "pickle"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "gguf"), []byte("GGUFtiny"), 0600); err != nil {
				t.Fatal(err)
			}
			locator := kind
			switch kind {
			case "directory":
				if err := os.Mkdir(filepath.Join(root, locator), 0700); err != nil {
					t.Fatal(err)
				}
			case "corrupt-container":
				if err := os.WriteFile(filepath.Join(root, "bad.zip"), []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
				locator = "bad.zip!/model.gguf"
			case "pickle":
				locator = "model.pkl"
			}
			src := &bridge.LocalStores{Adapters: []source.Adapter{childStore{root, []content.ManifestHolders{childManifest("a", locator), childManifest("b", "gguf")}}}}
			got, err := src.Resolve("ollama", "library/multi:latest", bridge.FileOnly)
			if err == nil || got.Locator == "gguf" {
				t.Fatalf("hidden %s failure: %+v, %v", kind, got, err)
			}
			if kind == "pickle" && !errors.Is(err, bridge.ErrPickle) {
				t.Fatalf("pickle: %v", err)
			}
			if kind == "missing" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("lost missing error: %v", err)
			}
		})
	}
}

func TestResolveOllamaAcceptedKindsTryTheNextChild(t *testing.T) {
	root := t.TempDir()
	writeZip(t, filepath.Join(root, "packed.zip"), member{name: "model.safetensors", data: []byte("tiny"), deflate: true})
	if err := os.WriteFile(filepath.Join(root, "gguf"), []byte("GGUFtiny"), 0600); err != nil {
		t.Fatal(err)
	}
	src := &bridge.LocalStores{Adapters: []source.Adapter{childStore{root, []content.ManifestHolders{childManifest("a", "packed.zip!/model.safetensors"), childManifest("b", "gguf")}}}}
	got, err := src.Resolve("ollama", "library/multi:latest", bridge.FileOnly)
	if err != nil || got.Locator != "gguf" {
		t.Fatalf("%+v, %v", got, err)
	}
	got, err = src.Resolve("ollama", "a", bridge.FileOnly)
	if !errors.Is(err, bridge.ErrUnaccepted) || got.Binding.Kind != bridge.BindCompressed {
		t.Fatalf("explicit compressed child: %+v, %v", got, err)
	}
}

func TestResolveOllamaKeepsFirstBindingAndOriginalRefusal(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"first": "GGUFtiny", "second": "GGUFtiny", "mlx": "TENSORS!"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	src := &bridge.LocalStores{Adapters: []source.Adapter{childStore{root, []content.ManifestHolders{childManifest("a", "first"), childManifest("b", "second")}}}}
	got, err := src.Resolve("ollama", "library/multi:latest", bridge.FileOnly)
	if err != nil || got.ID != "a" {
		t.Fatalf("first bindable child: %+v, %v", got, err)
	}
	got, err = src.Resolve("ollama", "library/multi:latest", bridge.Accepts{Kinds: []bridge.BindingKind{bridge.BindCompressed}})
	if !errors.Is(err, bridge.ErrUnaccepted) || got.ID != "a" {
		t.Fatalf("original unaccepted child: %+v, %v", got, err)
	}
	src = &bridge.LocalStores{Adapters: []source.Adapter{childStore{root, []content.ManifestHolders{childManifest("a", "mlx"), childManifest("b", "mlx")}}}}
	if _, err := src.Resolve("ollama", "library/multi:latest", bridge.FileOnly); !errors.Is(err, bridge.ErrUnknownObject) {
		t.Fatalf("all unsupported: %v", err)
	}
}

func (a childStore) Program() string { return "ollama" }
func (a childStore) Discover(source.Env) source.Discovery {
	return source.Discovery{Stores: []content.Store{{Name: "ollama", Program: "ollama", Locator: a.root, Present: true}}}
}
func (a childStore) Observe(s content.Store, _ source.Env) source.StoreObservation {
	return source.StoreObservation{Store: s, Manifests: a.manifests}
}
func childManifest(id, locator string) content.ManifestHolders {
	return content.ManifestHolders{Manifest: content.Manifest{Store: "ollama", ID: id,
		Names:   []content.Name{{Scheme: "ollama", Name: "library/multi:latest"}},
		Entries: []content.Entry{{Role: source.RoleWeights, Locator: locator, Size: 8}}}}
}
func TestResolveOllamaChildrenInBothManifestOrders(t *testing.T) {
	for _, first := range []string{"mlx", "gguf"} {
		t.Run(first, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range map[string]string{"mlx": "TENSORS!", "gguf": "GGUFtiny"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			second := "mlx"
			if first == "mlx" {
				second = "gguf"
			}
			src := &bridge.LocalStores{Adapters: []source.Adapter{childStore{root, []content.ManifestHolders{
				childManifest("a", first), childManifest("b", second),
			}}}}
			got, err := src.Resolve("ollama", "library/multi:latest", bridge.FileOnly)
			if err != nil || got.Locator != "gguf" || got.Path != filepath.Join(root, "gguf") {
				t.Fatalf("first %s: %+v, %v", first, got, err)
			}
			// An explicit child id never changes into its same-name sibling.
			id := "a"
			if first != "mlx" {
				id = "b"
			}
			if _, err := src.Resolve("ollama", id, bridge.FileOnly); !errors.Is(err, bridge.ErrUnknownObject) {
				t.Fatalf("explicit MLX child: %v", err)
			}
		})
	}
}
