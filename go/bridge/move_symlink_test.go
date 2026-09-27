//go:build !windows

package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExclusiveMovePreservesSymbolicLinkIdentity(t *testing.T) {
	for _, exists := range []bool{true, false} {
		t.Run(map[bool]string{true: "existing-target", false: "missing-target"}[exists], func(t *testing.T) {
			base := t.TempDir()
			target := filepath.Join(base, "target")
			if exists {
				if err := os.WriteFile(target, []byte("model"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			from, to := filepath.Join(base, "stage"), filepath.Join(base, "final")
			if err := os.Symlink(target, from); err != nil {
				t.Fatal(err)
			}
			before, err := identityOf(from)
			if err != nil {
				t.Fatal(err)
			}
			if err := moveNoReplace(from, to); err != nil {
				t.Fatal(err)
			}
			if got, err := identityOf(to); err != nil || got != before {
				t.Fatalf("link identity changed: %v -> %v (%v)", before, got, err)
			}
			if got, err := os.Readlink(to); err != nil || got != target {
				t.Fatalf("link target: %q (%v)", got, err)
			}
			if _, err := os.Lstat(from); !os.IsNotExist(err) {
				t.Fatalf("stage remains: %v", err)
			}
		})
	}
}
