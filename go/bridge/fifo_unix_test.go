//go:build unix

package bridge_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// A FIFO where an Ollama blob should be would block a plain open while Lend
// holds the bridge's lock. Resolve answers ErrUnknownObject at once
// (MODELBRIDGE-L21).
func TestAFIFOObjectIsNotBoundAndDoesNotBlock(t *testing.T) {
	root := t.TempDir()
	locator := "blobs/sha256-" + strings.Repeat("d", 64)
	if err := os.MkdirAll(filepath.Join(root, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, filepath.FromSlash(locator)), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	src := &bridge.LocalStores{Adapters: []source.Adapter{strayStore{
		store:   content.Store{Name: "ollama", Program: "ollama", Locator: root, Present: true},
		locator: locator}}}
	done := make(chan error, 1)
	go func() {
		_, err := src.Resolve("ollama", locator, bridge.FileOnly)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, bridge.ErrUnknownObject) {
			t.Fatalf("a FIFO resolved: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resolving a FIFO blocked")
	}
}
