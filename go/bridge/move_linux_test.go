package bridge

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestExclusiveFileMovePreservesOccupiedDestination(t *testing.T) {
	base := t.TempDir()
	from, to := filepath.Join(base, "owned"), filepath.Join(base, "foreign")
	if err := os.WriteFile(from, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := moveNoReplace(from, to); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("exclusive file move: %v", err)
	}
	for path, want := range map[string]string{from: "owned", to: "foreign"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("%s: %q, %v; want %q", path, data, err, want)
		}
	}
}

// An editor or engine can atomically replace the final path during undo.
// Every interleaving must retain the foreign bytes at either the final or
// quarantined name. The race loop exercises timing; the syscall supplies the
// atomicity guarantee even when this test observes only some interleavings.
func TestExclusiveFileMovePreservesConcurrentReplacement(t *testing.T) {
	base := t.TempDir()
	from := filepath.Join(base, "final")
	to := filepath.Join(base, "quarantined")
	replacement := filepath.Join(base, "replacement")
	for i := 0; i < 2000; i++ {
		if err := os.WriteFile(from, []byte("owned"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(replacement, []byte("foreign"), 0o600); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			<-start
			done <- os.Rename(replacement, from)
		}()
		close(start)
		moveErr := moveNoReplace(from, to)
		if replaceErr := <-done; replaceErr != nil {
			t.Fatalf("iteration %d, replacement: %v", i, replaceErr)
		}
		if moveErr != nil {
			t.Fatalf("iteration %d, exclusive move: %v", i, moveErr)
		}
		foreignFound := false
		for _, path := range []string{from, to} {
			data, err := os.ReadFile(path)
			if err == nil && string(data) == "foreign" {
				foreignFound = true
			}
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("iteration %d, read %s: %v", i, path, err)
			}
		}
		if !foreignFound {
			t.Fatalf("iteration %d: concurrent replacement was lost", i)
		}
		for _, path := range []string{from, to} {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
}
