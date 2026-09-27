package bridge_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"

	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
)

// comfyuiEnv is a ComfyUI engine pointed at one operator-configured install
// directory, the way modelbridged's own --config would name one.
func comfyuiEnv(install string) source.Env {
	return source.Env{Config: source.Config{ComfyUI: []string{install}}}
}

// KindOf reads a kind only from an object's own name inside a native ComfyUI
// store: the leading path segment ComfyUI itself filed it under. Every other
// store, including a comfyui-extra claim that points at another program's own
// folder layout, names no kind rather than guessing one.
func TestKindOfClassifiesOnlyNativeComfyUIStores(t *testing.T) {
	for _, c := range []struct{ store, locator, want string }{
		{"comfyui", "checkpoints/sd15.safetensors", "checkpoints"},
		{"comfyui", "text_encoders/umt5_xxl_fp16.safetensors", "text_encoders"},
		{"comfyui#2", "loras/style.safetensors", "loras"},
		{"comfyui-extra:a111", "models/Stable-diffusion/v1-5.safetensors", ""},
		{"huggingface", "org/repo/model.safetensors", ""},
		{"comfyui", "sd15.safetensors", ""},
	} {
		if got := bridge.KindOf(c.store, c.locator); got != c.want {
			t.Errorf("KindOf(%q, %q) = %q, wanted %q", c.store, c.locator, got, c.want)
		}
	}
}

// The table test of the kind-to-folder mapping: every kind this provider
// classifies lends under the ComfyUI folder of the same name, inside
// openabstractions-lend, the same shape MODELBRIDGE-L7 already states for LM Studio.
func TestComfyUIRootMapsEveryKnownKindToItsFolder(t *testing.T) {
	root := t.TempDir()
	for _, folder := range bridge.ComfyUIFolders {
		if err := os.MkdirAll(filepath.Join(root, "models", folder), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	eng := bridge.ComfyUI{Env: comfyuiEnv(root)}
	for kind, folder := range bridge.ComfyUIFolders {
		got, err := eng.Root(bridge.Object{Kind: kind, File: kind + ".safetensors"})
		if err != nil {
			t.Fatalf("kind %s: %v", kind, err)
		}
		want := filepath.Join(root, "models", folder, bridge.LendDirectory)
		if got != want {
			t.Errorf("kind %s: root %q, wanted %q", kind, got, want)
		}
	}
}

// A kind this provider does not classify into one of ComfyUI's folders, and
// an object with no classified kind at all, are each refused by the engine
// rather than placed by a guess. hypernetworks is a real ComfyUI folder this
// table does not name, which is the point: the table, not ComfyUI's own
// vocabulary, decides what this provider lends.
func TestComfyUIRootRefusesAnUnmappableOrUnclassifiedKind(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	eng := bridge.ComfyUI{Env: comfyuiEnv(root)}
	for _, o := range []bridge.Object{
		{Kind: "hypernetworks", File: "style.pt"},
		{Kind: "", File: "unknown.safetensors"},
	} {
		if _, err := eng.Root(o); err == nil {
			t.Errorf("object %+v: an unmappable kind was accepted", o)
		}
	}
}

// comfyuiMachine lays out a checkpoint in one native ComfyUI store and a
// second installed ComfyUI's own models tree, with no extra_model_paths.yaml,
// the shape a lend between two installations is.
func comfyuiMachine(t *testing.T) (store, bridge.ComfyUI, string, string) {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "store")
	if err := os.MkdirAll(filepath.Join(src, "checkpoints"), 0o755); err != nil {
		t.Fatal(err)
	}
	locator := "checkpoints/sd15.safetensors"
	if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(locator)), []byte("checkpoint weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(base, "comfyui")
	if err := os.MkdirAll(filepath.Join(install, "models", "checkpoints"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := store{name: "comfyui", root: src, objects: map[string]string{"sd15": locator}}
	eng := bridge.ComfyUI{Env: comfyuiEnv(install)}
	return st, eng, filepath.Join(base, "state", "lends.json"), install
}

// A checkpoint lends into ComfyUI's own checkpoints folder by link, with no
// edit to that install's extra_model_paths.yaml, and Unlend removes exactly
// what the lend created.
func TestLendAndUnlendAgainstAComfyUIShapedTree(t *testing.T) {
	src, eng, ledgerPath, install := comfyuiMachine(t)
	b := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)

	lent := b.Lend("comfyui", "sd15", "comfyui", false)
	if lent.Outcome != wire.LendOutcomeLent {
		t.Fatalf("lend %s: %s", lent.Outcome, lent.Detail)
	}
	want := filepath.Join(install, "models", "checkpoints", bridge.LendDirectory, "sd15", "sd15.safetensors")
	if lent.Lend.Link != want {
		t.Fatalf("link %s, wanted %s", lent.Lend.Link, want)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "checkpoint weights" {
		t.Fatalf("ComfyUI cannot read the lent file: %v %q", err, data)
	}
	if _, err := os.Stat(filepath.Join(install, "extra_model_paths.yaml")); !os.IsNotExist(err) {
		t.Fatalf("a lend wrote extra_model_paths.yaml: %v", err)
	}

	unlent := b.Unlend(lent.Lend.ID)
	if unlent.Outcome != wire.UnlendOutcomeUnlent {
		t.Fatalf("unlend %s: %s", unlent.Outcome, unlent.Detail)
	}
	if _, err := os.Lstat(want); !os.IsNotExist(err) {
		t.Fatalf("the link is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(install, "models", "checkpoints", bridge.LendDirectory)); !os.IsNotExist(err) {
		t.Fatalf("the lend directory was not pruned")
	}
	if _, err := os.Stat(filepath.Join(install, "models", "checkpoints")); err != nil {
		t.Fatalf("ComfyUI's own checkpoints folder was removed: %v", err)
	}
}

// An object no store classifies into a ComfyUI kind is refused
// unsupported_engine rather than placed by a guess.
func TestLendToComfyUIRefusesAnObjectWithNoClassifiedKind(t *testing.T) {
	src, _, ledgerPath := machine(t)
	install := t.TempDir()
	if err := os.MkdirAll(filepath.Join(install, "models", "checkpoints"), 0o755); err != nil {
		t.Fatal(err)
	}
	eng := bridge.ComfyUI{Env: comfyuiEnv(install)}
	b := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)

	result := b.Lend("huggingface", "manifest-1", "comfyui", false)
	if result.Outcome != wire.LendOutcomeUnsupportedEngine {
		t.Fatalf("lend %s: %s", result.Outcome, result.Detail)
	}
	if result.Detail == "" {
		t.Fatal("an unsupported kind carries no reason")
	}
}

// A kind ComfyUI itself keeps a folder for, but this provider's table does
// not name, is unsupported_engine end to end through Lend, the same typed
// outcome the contract already carries for an engine a file cannot be placed
// with.
func TestLendRefusesAKindComfyUIHasNoFolderFor(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "store")
	if err := os.MkdirAll(filepath.Join(src, "hypernetworks"), 0o755); err != nil {
		t.Fatal(err)
	}
	locator := "hypernetworks/style.pt"
	if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(locator)), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(base, "comfyui")
	if err := os.MkdirAll(filepath.Join(install, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := store{name: "comfyui", root: src, objects: map[string]string{"style": locator}}
	eng := bridge.ComfyUI{Env: comfyuiEnv(install)}
	b := bridge.New(st, open(t, filepath.Join(base, "state", "lends.json")), bridge.OSLinker(), eng)

	result := b.Lend("comfyui", "style", "comfyui", false)
	if result.Outcome != wire.LendOutcomeUnsupportedEngine {
		t.Fatalf("lend %s: %s", result.Outcome, result.Detail)
	}
	if result.Detail == "" {
		t.Fatal("an unsupported kind carries no reason")
	}
}
