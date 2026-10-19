package bridge_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

// OA never loads a pickle (MODELBRIDGE-L20, storage-over-local-stores LS-1),
// and the one source every caller resolves through refuses to bind one. A
// .pth upscaler and a torch.save .bin in a native ComfyUI store are refused
// object:pickle when lent by locator, with or without copy, and nothing
// reaches the target ComfyUI's folders; a GGUF .bin beside them still lends,
// and a whisper.cpp ggml .bin resolves.
func TestLendRefusesPickles(t *testing.T) {
	base := t.TempDir()
	srcInstall := filepath.Join(base, "source-comfyui")
	files := map[string][]byte{
		"main.py":                                 []byte("# ComfyUI"),
		"models/upscale_models/4x-esrgan.pth":     []byte("pickled upscaler"),
		"models/upscale_models/pytorch_model.bin": {'P', 'K', 0x03, 0x04, 0, 0},
		"models/upscale_models/ggml-upscaler.bin": []byte("GGUF\x03\x00\x00\x00"),
		"models/upscale_models/ggml-medium.bin":   []byte("lmgg\x80\xb1\x00\x00"),
	}
	for rel, body := range files {
		p := filepath.Join(srcInstall, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	install := filepath.Join(base, "comfyui")
	folder := filepath.Join(install, "models", "upscale_models")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	env := source.Env{GOOS: runtime.GOOS, Home: filepath.Join(base, "home"), Getenv: func(string) string { return "" },
		Config: source.Config{ComfyUI: []string{srcInstall}}}
	src := bridge.NewLocalStores(env)
	b := bridge.New(src, open(t, filepath.Join(base, "state", "lends.json")), bridge.OSLinker(), bridge.ComfyUI{Env: comfyuiEnv(install)})

	for _, id := range []string{"upscale_models/4x-esrgan.pth", "upscale_models/pytorch_model.bin"} {
		if _, err := src.Resolve("comfyui", id, bridge.Accepts{Kinds: []bridge.BindingKind{bridge.BindRegion, bridge.BindFile}}); !errors.Is(err, bridge.ErrPickle) {
			t.Fatalf("%s resolved: %v", id, err)
		}
		for _, copy := range []bool{false, true} {
			got := b.Lend("comfyui", id, "comfyui", copy)
			if got.Outcome != wire.LendOutcomeNotPermitted || got.Lend != nil || !strings.HasPrefix(got.Detail, bridge.ReasonPickle) {
				t.Fatalf("%s copy %v: %+v", id, copy, got)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(folder, bridge.LendDirectory)); !os.IsNotExist(err) {
		t.Fatalf("a refused pickle reached ComfyUI's folder: %v", err)
	}
	if got := b.Lend("comfyui", "upscale_models/ggml-upscaler.bin", "comfyui", false); got.Outcome != wire.LendOutcomeLent {
		t.Fatalf("a GGUF .bin: %+v", got)
	}
	// whisper.cpp's ggml .bin is a model format, never a pickle (LS-1,
	// MODELBRIDGE-L20, MODELBRIDGE-L21).
	if _, err := src.Resolve("comfyui", "upscale_models/ggml-medium.bin", bridge.Accepts{Kinds: []bridge.BindingKind{bridge.BindFile}}); err != nil {
		t.Fatalf("a ggml .bin: %v", err)
	}
}

// A pickle member of a zip is refused like a loose one, judged by its name
// alone (LS-9), even for a caller that reads regions.
func TestResolveRefusesAPickleMember(t *testing.T) {
	src, root, _ := packages(t)
	writeZip(t, filepath.Join(root, "pickles.zip"), member{name: "enc.pth", data: []byte("pickled"), align: 64})
	_, err := src.Resolve("packages", "pickles.zip!/enc.pth", bridge.Accepts{Kinds: []bridge.BindingKind{bridge.BindRegion}})
	if !errors.Is(err, bridge.ErrPickle) {
		t.Fatalf("a pickle member resolved: %v", err)
	}
}
