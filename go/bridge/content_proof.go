package bridge

import (
	"os"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// ContentProof optionally supplies current hashed evidence for one exact
// singleton manifest ID. Source implementations need not implement it. It reads
// inventoryd's existing digest record and revalidates its held-file stamp; it
// hashes no bytes and schedules no work. Missing or stale evidence answers false.
func (l *LocalStores) ContentProof(store, id string) (content.Object, bool) {
	result := l.take()
	root := ""
	for _, s := range result.Page.Stores {
		if s.Name == store && s.Present && len(s.Errors) == 0 && s.Domain == "" && s.Placement == content.PlacementLocal {
			root = s.Locator
		}
	}
	if root == "" {
		return content.Object{}, false
	}
	for _, mh := range result.Page.Manifests {
		m := mh.Manifest
		if m.Store != store || m.ID != id || m.Kind != source.KindModel || m.SupersededBy != "" || len(m.Entries) != 1 {
			continue
		}
		e := m.Entries[0]
		if e.Role != "" && e.Role != source.RoleWeights {
			return content.Object{}, false
		}
		o, err := object(store, id, e.Locator, root, "", "", e.Size, FileOnly)
		if err != nil || o.Binding.Kind != BindFile {
			return content.Object{}, false
		}
		info, err := os.Stat(o.Path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != e.Size {
			return content.Object{}, false
		}
		digests := l.Env.Digests
		if digests == nil {
			// Read the source owner's current record without installing a
			// hashing environment into discovery or keeping a second cache.
			digests = source.NewDigests(l.Env.DigestRecordPath())
		}
		sum, current := digests.Recorded(o.Path, info.Size(), info.ModTime())
		if !current {
			return content.Object{}, false
		}
		return content.Object{Locator: e.Locator, Size: e.Size, Modified: source.Stamp(info.ModTime()), Digest: "sha256:" + sum, Evidence: content.EvidenceHashed}, true
	}
	return content.Object{}, false
}
