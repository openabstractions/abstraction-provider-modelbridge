package bridge_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"golang.org/x/sys/windows"
)

func TestResolveOllamaUnreadableChildIsNotSkipped(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"blocked", "gguf"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("GGUFtiny"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := windows.UTF16PtrFromString(filepath.Join(root, "blocked"))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Error(err)
		}
	}()
	src := &bridge.LocalStores{Adapters: []source.Adapter{childStore{root, []content.ManifestHolders{childManifest("a", "blocked"), childManifest("b", "gguf")}}}}
	got, err := src.Resolve("ollama", "library/multi:latest", bridge.FileOnly)
	if err == nil || got.Locator == "gguf" || !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("unreadable first child: %+v, %v", got, err)
	}
}
