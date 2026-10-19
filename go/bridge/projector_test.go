package bridge_test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/openabstractions/abstraction-identity/placetest"
	bridge "github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/huggingface"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// ggufHeader is a GGUF v3 file of string and uint32 values, then filler.
func ggufHeader(strs map[string]string, nums map[string]uint32) []byte {
	out := append([]byte(nil), "GGUF"...)
	out = binary.LittleEndian.AppendUint32(out, 3)
	out = binary.LittleEndian.AppendUint64(out, 1)
	out = binary.LittleEndian.AppendUint64(out, uint64(len(strs)+len(nums)))
	str := func(s string) {
		out = binary.LittleEndian.AppendUint64(out, uint64(len(s)))
		out = append(out, s...)
	}
	for k, v := range strs {
		str(k)
		out = binary.LittleEndian.AppendUint32(out, 8)
		str(v)
	}
	for k, v := range nums {
		str(k)
		out = binary.LittleEndian.AppendUint32(out, 4)
		out = binary.LittleEndian.AppendUint32(out, v)
	}
	return append(out, make([]byte, 4096)...)
}

func textModel(width uint32) []byte {
	return ggufHeader(map[string]string{"general.architecture": "qwen3", "general.type": "model"}, map[string]uint32{"qwen3.embedding_length": width})
}

func projector(width uint32) []byte {
	return ggufHeader(map[string]string{"general.architecture": "clip", "general.type": "mmproj"}, map[string]uint32{"clip.vision.projection_dim": width})
}

func hfSnapshot(t *testing.T, home, repo string, files map[string][]byte) {
	t.Helper()
	snap := filepath.Join(home, ".cache", "huggingface", "hub", "models--acme--"+repo, "snapshots", "0123456789abcdef0123456789abcdef01234567")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(snap, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func projectorTestStore(home string) *bridge.LocalStores {
	return bridge.NewLocalStores(source.Env{GOOS: runtime.GOOS, Home: home, Getenv: func(string) string { return "" }})
}

func hfObject(t *testing.T, src *bridge.LocalStores, repo, file string) bridge.Object {
	t.Helper()
	locator := "models--acme--" + repo + "/snapshots/0123456789abcdef0123456789abcdef01234567/" + file
	o, err := src.Resolve("huggingface", locator, bridge.FileOnly)
	if err != nil {
		t.Fatalf("resolve %s: %v", locator, err)
	}
	return o
}

// A Hugging Face snapshot keeps its projector in a manifest of its own beside
// the model: the model's projector is that sibling, F16 before BF16, and a
// repository with no projector names none.
func TestAHuggingFaceModelFindsItsSiblingProjector(t *testing.T) {
	home := t.TempDir()
	hfSnapshot(t, home, "Vision-GGUF", map[string][]byte{
		"Vision-35B-A3B-UD-Q4_K_XL.gguf": textModel(2048),
		"Vision-35B-A3B-Q8_0.gguf":       textModel(2048),
		"mmproj-BF16.gguf":               projector(2048),
		"mmproj-F16.gguf":                projector(2048),
	})
	hfSnapshot(t, home, "Text-GGUF", map[string][]byte{"Text-0.5B-Q4_K_M.gguf": textModel(896)})
	src := projectorTestStore(home)
	var _ bridge.Projectors = src

	for _, file := range []string{"Vision-35B-A3B-UD-Q4_K_XL.gguf", "Vision-35B-A3B-Q8_0.gguf"} {
		got, ok := src.Projector(hfObject(t, src, "Vision-GGUF", file))
		if !ok || got.File != "mmproj-F16.gguf" || got.Binding.Kind != bridge.BindFile || got.Path == "" || got.Size != int64(len(projector(2048))) {
			t.Fatalf("%s projector = %+v %v, want mmproj-F16.gguf", file, got, ok)
		}
	}
	if got, ok := src.Projector(hfObject(t, src, "Text-GGUF", "Text-0.5B-Q4_K_M.gguf")); ok {
		t.Fatalf("a text-only repository named projector %+v", got)
	}
}

// A directory holding two families pairs a projector only with the model
// whose embedding width it projects to.
func TestAProjectorOfAnotherWidthIsNeverChosen(t *testing.T) {
	home := t.TempDir()
	hfSnapshot(t, home, "Mixed-GGUF", map[string][]byte{
		"alpha-Q4_K_M.gguf": textModel(2048),
		"beta-Q4_K_M.gguf":  textModel(4096),
		"mmproj-F16.gguf":   projector(4096),
	})
	src := projectorTestStore(home)
	if got, ok := src.Projector(hfObject(t, src, "Mixed-GGUF", "alpha-Q4_K_M.gguf")); ok {
		t.Fatalf("alpha took a 4096-wide projector: %+v", got)
	}
	if got, ok := src.Projector(hfObject(t, src, "Mixed-GGUF", "beta-Q4_K_M.gguf")); !ok || got.File != "mmproj-F16.gguf" {
		t.Fatalf("beta projector = %+v %v", got, ok)
	}
	model := hfObject(t, src, "Mixed-GGUF", "alpha-Q4_K_M.gguf")
	model.Path = filepath.Join(t.TempDir(), "foreign.gguf")
	if err := os.WriteFile(model.Path, textModel(4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := src.Projector(model); ok {
		t.Fatalf("a foreign weights path changed alpha's width: %+v", got)
	}
}

type projectorSnapshotSwap struct {
	source.Adapter
	swap func()
}

func (a projectorSnapshotSwap) Observe(store content.Store, env source.Env) source.StoreObservation {
	result := a.Adapter.Observe(store, env)
	a.swap()
	return result
}

func TestProjectorRefusesSnapshotDirectoryAliases(t *testing.T) {
	t.Run("outside store", func(t *testing.T) { projectorDirectoryReplacement(t, false) })
	t.Run("inside store", func(t *testing.T) { projectorDirectoryReplacement(t, true) })
}

func projectorDirectoryReplacement(t *testing.T, inside bool) {
	t.Helper()
	home, outside := t.TempDir(), t.TempDir()
	hfSnapshot(t, home, "Vision-GGUF", map[string][]byte{
		"Vision-35B-Q8_0.gguf": textModel(2048),
		"mmproj-F16.gguf":      projector(2048),
	})
	src := projectorTestStore(home)
	model := hfObject(t, src, "Vision-GGUF", "Vision-35B-Q8_0.gguf")
	snapshot := filepath.Dir(model.Path)
	if inside {
		outside = filepath.Join(src.Root(model.Store), "retargeted")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string][]byte{model.File: textModel(2048), "mmproj-F16.gguf": projector(2048)} {
		if err := os.WriteFile(filepath.Join(outside, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	swapped := false
	src.Adapters = []source.Adapter{projectorSnapshotSwap{Adapter: huggingface.New(), swap: func() {
		if swapped {
			return
		}
		swapped = true
		if err := os.Rename(snapshot, snapshot+"-original"); err != nil {
			t.Fatal(err)
		}
		projectorTestLink(t, snapshot, outside, true)
	}}}
	if got, ok := src.Projector(model); !swapped || ok {
		t.Fatalf("projector after directory alias replacement = %+v %v, swapped=%v", got, ok, swapped)
	}
}

func TestProjectorUsesHuggingFaceBlobLinkInsideStore(t *testing.T) {
	home := t.TempDir()
	hfSnapshot(t, home, "Vision-GGUF", map[string][]byte{
		"Vision-35B-Q8_0.gguf": textModel(2048),
		"mmproj-F16.gguf":      projector(2048),
	})
	src := projectorTestStore(home)
	model := hfObject(t, src, "Vision-GGUF", "Vision-35B-Q8_0.gguf")
	snapshot := filepath.Dir(model.Path)
	blob := filepath.Join(filepath.Dir(filepath.Dir(snapshot)), "blobs", "00112233445566778899aabbccddeeff00112233")
	if err := os.MkdirAll(filepath.Dir(blob), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, projector(2048), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(snapshot, "mmproj-F16.gguf")
	target := filepath.Join("..", "..", "blobs", filepath.Base(blob))
	// Once the adapter sees a symlink it publishes the canonical blob
	// locator. Replace the copy after Observe to exercise the retained
	// snapshot locator while its content is still in the same store.
	swapped := false
	src.Adapters = []source.Adapter{projectorSnapshotSwap{Adapter: huggingface.New(), swap: func() {
		if swapped {
			return
		}
		swapped = true
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		projectorTestLink(t, link, target, false)
	}}}
	if got, ok := src.Projector(model); !swapped || !ok || got.Path != blob || got.Binding.Path != blob || got.File != "mmproj-F16.gguf" {
		t.Fatalf("store blob projector = %+v %v, want %s", got, ok, blob)
	}
}

func projectorTestLink(t *testing.T, link, target string, directory bool) {
	t.Helper()
	var err error
	if directory && runtime.GOOS == "windows" {
		err = placetest.Junction(link, target)
	} else {
		err = os.Symlink(target, link)
	}
	if err != nil {
		if !directory && (errors.Is(err, os.ErrPermission) || runtime.GOOS == "windows") {
			t.Skipf("fixture cannot create a file symlink: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
	})
}
