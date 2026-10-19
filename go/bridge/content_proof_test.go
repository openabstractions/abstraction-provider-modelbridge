package bridge_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

func TestOptionalContentProofChecksCurrentStampWithoutHashing(t *testing.T) {
	src, _, _ := descriptorFixture(t, "family")
	adapter := src.Adapters[0].(descriptorStore)
	adapter.store.Placement = content.PlacementLocal
	src.Adapters[0] = adapter
	if _, ok := src.ContentProof("jan", "manifest-1"); ok {
		t.Fatal("missing record proved content")
	}
	file := filepath.Join(adapter.store.Locator, "model.gguf")
	opened, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	stamp, _, err := identity.FileStamp(opened)
	opened.Close()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "private")
	if err := privatedir.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "digests.json")
	// A prepared source-record fixture supplies evidence; this test hashes
	// no bytes and starts no source hash worker.
	data, err := json.Marshal(map[string]any{"version": 3, "files": []map[string]any{{"path": file, "size": info.Size(), "modified": info.ModTime().UTC().Format(time.RFC3339Nano), "stamp": stamp, "sha256": strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, data, 0600); err != nil {
		t.Fatal(err)
	}
	src.Env.Digests = source.NewDigests(record)
	proof, ok := src.ContentProof("jan", "manifest-1")
	if !ok || proof.Evidence != content.EvidenceHashed || proof.Digest != "sha256:"+strings.Repeat("a", 64) || proof.Size != 7 || proof.Modified == "" {
		t.Fatalf("proof %+v %v", proof, ok)
	}
	if _, ok := src.ContentProof("jan", "publisher/other"); ok {
		t.Fatal("a name was accepted as an opaque object ID")
	}
	if _, ok := src.ContentProof("other-store", "manifest-1"); ok {
		t.Fatal("wrong store proved content")
	}
	adapter.manifest.Entries = append(adapter.manifest.Entries, adapter.manifest.Entries[0])
	src.Adapters[0] = adapter
	if _, ok := src.ContentProof("jan", "manifest-1"); ok {
		t.Fatal("multi-entry collection proved one object")
	}
	adapter.manifest.Entries = adapter.manifest.Entries[:1]
	src.Adapters[0] = adapter
	if err := os.Rename(file, file+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, ok := src.ContentProof("jan", "manifest-1"); ok {
		t.Fatal("same-size replacement with old time retained proof")
	}
}
