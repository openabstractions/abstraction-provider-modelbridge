package bridge

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/huggingface"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/container"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/gguf"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// Projectors is a Source that finds a model object's vision projector, the
// mmproj file an engine loads beside the model's weights for image and audio
// input. A Source without it names no projector.
type Projectors interface {
	// Projector is model's projector in model's own store, bound as a whole
	// file. False when the store holds none this source can tell belongs
	// to model.
	Projector(model Object) (Object, bool)
}

// Projector finds model's projector in its store, in this order:
//   - a projector entry in the model's own manifest (a projector layer
//     or an explicit projector path recorded by the store adapter);
//   - else a projector manifest beside the model's weights file, in the same
//     directory (for example, mmproj-F16.gguf). Such a sibling
//     is a projector by its entry role, its descriptor's component role, its
//     name (source.RoleOf) or its GGUF header (general.type "mmproj" or the
//     clip architecture).
//
// A sibling belongs to model when its projection width equals the model's
// embedding width, when its name carries the model's family, or when the
// directory holds no weights of another family. A sibling whose width
// differs from the model's is never chosen: the engine refuses that pair.
// Among several, the choice is deterministic: a width match first, then a
// name match, then precision F16, BF16, F32, then any other (F16 is what the
// converter writes by default and what published repositories ship; a
// quantized projector loses image detail), then locator order.
func (l *LocalStores) Projector(model Object) (Object, bool) {
	if model.Store == "" || model.Locator == "" || model.Binding.Kind != BindFile || model.Path == "" {
		return Object{}, false
	}
	result := l.take()
	var store content.Store
	for _, s := range result.Page.Stores {
		if s.Name == model.Store {
			store = s
		}
	}
	root := store.Locator
	if root == "" {
		return Object{}, false
	}
	heldRoot, err := fileid.Open(context.Background(), fileid.Self, root, fileid.Options{})
	if err != nil {
		return Object{}, false
	}
	defer heldRoot.Close()
	if !heldRoot.FileID().IsDir() {
		return Object{}, false
	}
	files := projectorFiles{root: heldRoot, huggingFace: store.Program == huggingface.New().Program()}
	modelLocator := filepath.ToSlash(model.Locator)
	own := func(m content.Manifest) bool {
		if model.ID != "" && m.ID == model.ID {
			return true
		}
		for _, e := range m.Entries {
			if filepath.ToSlash(e.Locator) == modelLocator {
				return true
			}
		}
		return false
	}
	var owned []projectorCandidate
	family := Family(model.File)
	for _, mh := range result.Page.Manifests {
		m := mh.Manifest
		if m.Store != model.Store || !own(m) {
			continue
		}
		if m.Kind == source.KindModel && m.Descriptor["family"] != "" {
			family = m.Descriptor["family"]
		}
		for _, e := range m.Entries {
			if e.Role == source.RoleProjector && e.Locator != "" && filepath.ToSlash(e.Locator) != modelLocator {
				owned = append(owned, projectorCandidate{id: m.ID, locator: e.Locator, size: e.Size})
			}
		}
	}
	if len(owned) > 0 {
		return bindProjector(model.Store, root, files, owned)
	}
	dir := path.Dir(modelLocator)
	var siblings []projectorCandidate
	otherFamily := false
	for _, mh := range result.Page.Manifests {
		m := mh.Manifest
		if m.Store != model.Store || own(m) || m.SupersededBy != "" {
			continue
		}
		entry, ok := weights(m)
		if !ok || path.Dir(filepath.ToSlash(entry.Locator)) != dir {
			continue
		}
		if projectorManifest(m, entry, files) {
			siblings = append(siblings, projectorCandidate{id: m.ID, locator: entry.Locator, size: entry.Size})
			continue
		}
		other := Family(path.Base(filepath.ToSlash(entry.Locator)))
		if m.Kind == source.KindModel && m.Descriptor["family"] != "" {
			other = m.Descriptor["family"]
		}
		if other != family && other != Family(model.File) {
			otherFamily = true
		}
	}
	if len(siblings) == 0 {
		return Object{}, false
	}
	width := uint64(0)
	modelFile, err := files.open(model.Locator)
	if err != nil {
		return Object{}, false
	}
	defer modelFile.Close()
	if f, err := gguf.ReadFactsFrom(modelFile.File()); err == nil {
		width = f.EmbeddingLength
	}
	nameFamily := strings.ToLower(Family(model.File))
	var accepted []projectorCandidate
	for _, c := range siblings {
		if width > 0 {
			file, err := files.open(c.locator)
			if err != nil {
				continue
			}
			f, err := gguf.ReadFactsFrom(file.File())
			file.Close()
			if err == nil && f.ProjectionDim > 0 {
				if f.ProjectionDim != width {
					continue
				}
				c.widthMatch = true
			}
		}
		c.nameMatch = nameFamily != "" && strings.Contains(strings.ToLower(path.Base(filepath.ToSlash(c.locator))), nameFamily)
		if c.widthMatch || c.nameMatch || !otherFamily {
			accepted = append(accepted, c)
		}
	}
	return bindProjector(model.Store, root, files, accepted)
}

type projectorCandidate struct {
	id, locator           string
	size                  int64
	widthMatch, nameMatch bool
}

// precision ranks a projector file's precision token: F16, BF16, F32, other.
func precision(locator string) int {
	name := strings.ToLower(path.Base(filepath.ToSlash(locator)))
	switch {
	case strings.Contains(name, "bf16"):
		return 1
	case strings.Contains(name, "f16"):
		return 0
	case strings.Contains(name, "f32"):
		return 2
	}
	return 3
}

// bindProjector binds the best of candidates as a whole file in a model
// format, trying the next when one does not bind.
func bindProjector(store, root string, files projectorFiles, candidates []projectorCandidate) (Object, bool) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.widthMatch != b.widthMatch {
			return a.widthMatch
		}
		if a.nameMatch != b.nameMatch {
			return a.nameMatch
		}
		if pa, pb := precision(a.locator), precision(b.locator); pa != pb {
			return pa < pb
		}
		return a.locator < b.locator
	})
	for _, c := range candidates {
		file, err := files.open(c.locator)
		if err != nil {
			continue
		}
		base := path.Base(c.locator)
		if !projectorFormat(file.File(), base) {
			file.Close()
			continue
		}
		currentFile, err := files.open(c.locator)
		same := err == nil && file.FileID().Equal(currentFile.FileID())
		full := currentFile.FileID().Path()
		file.Close()
		currentFile.Close()
		if !same {
			continue
		}
		current, err := fileid.Resolve(context.Background(), fileid.Self, root, fileid.Options{})
		if err != nil || !current.Equal(files.root.FileID()) {
			return Object{}, false
		}
		return Object{Store: store, ID: c.id, Locator: c.locator, File: base, Family: FamilyOf("", base),
			Size: c.size, Kind: KindOf(store, c.locator), Path: full,
			Binding: ObjectBinding{Kind: BindFile, Path: full, Length: c.size}}, true
	}
	return Object{}, false
}

// projectorManifest reports whether a manifest's weights entry is a vision
// projector: by its entry role, its descriptor's component role, its name,
// or its GGUF header.
func projectorManifest(m content.Manifest, entry content.Entry, files projectorFiles) bool {
	if entry.Role == source.RoleProjector || m.Descriptor["role"] == source.RoleProjector || source.RoleOf(entry.Locator) == source.RoleProjector {
		return true
	}
	if !strings.HasSuffix(strings.ToLower(entry.Locator), ".gguf") {
		return false
	}
	file, err := files.open(entry.Locator)
	if err != nil {
		return false
	}
	defer file.Close()
	g, err := gguf.ReadFrom(file.File())
	return err == nil && (g.Type == "mmproj" || g.Architecture == "clip")
}

type projectorFiles struct {
	root        fileid.Handle
	huggingFace bool
}

func (files projectorFiles) open(locator string) (fileid.Handle, error) {
	if _, _, member := container.SplitMemberLocator(locator); member {
		return fileid.Handle{}, fs.ErrInvalid
	}
	read := fileid.Options{Read: true}
	f, err := files.root.Beneath(locator, read)
	if files.huggingFace && errors.Is(err, fileid.ErrLink) {
		f, err = files.openSnapshotBlob(locator, read)
	}
	if err != nil {
		return fileid.Handle{}, err
	}
	info, err := f.File().Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return fileid.Handle{}, fs.ErrInvalid
	}
	relation, err := f.Relate(files.root)
	if err != nil || relation.Kind != fileid.RelationInside {
		f.Close()
		return fileid.Handle{}, fs.ErrPermission
	}
	return f, nil
}

var (
	snapshotCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
	snapshotBlob   = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// openSnapshotBlob admits the leaf symlink used by Hugging Face's cache.
// Both its parent and its same-repository blob are opened without links.
func (files projectorFiles) openSnapshotBlob(locator string, read fileid.Options) (fileid.Handle, error) {
	parts := strings.Split(locator, "/")
	if len(parts) < 4 || !strings.HasPrefix(parts[0], huggingface.RepoPrefix) ||
		parts[1] != "snapshots" || !snapshotCommit.MatchString(parts[2]) {
		return fileid.Handle{}, fileid.ErrLink
	}
	parent, err := files.root.Beneath(path.Dir(locator), fileid.Options{})
	if err != nil {
		return fileid.Handle{}, err
	}
	defer parent.Close()
	dir, err := os.OpenRoot(parent.FileID().Path())
	if err != nil {
		return fileid.Handle{}, err
	}
	defer dir.Close()
	opened, err := dir.Open(".")
	if err != nil {
		return fileid.Handle{}, err
	}
	defer opened.Close()
	key, err := fileid.FileKeyOf(opened)
	if err != nil || key != parent.FileID().Key() {
		return fileid.Handle{}, fs.ErrPermission
	}
	target, err := dir.Readlink(parts[len(parts)-1])
	if err != nil {
		return fileid.Handle{}, err
	}
	digest, ok := strings.CutPrefix(filepath.ToSlash(target), strings.Repeat("../", len(parts)-2)+"blobs/")
	if !ok || !snapshotBlob.MatchString(digest) {
		return fileid.Handle{}, fileid.ErrLink
	}
	return files.root.Beneath(path.Join(parts[0], "blobs", digest), read)
}

func projectorFormat(file io.Reader, name string) bool {
	if source.ModelFormatOf("", name) != "" {
		return true
	}
	// A .bin may be a native model when its bytes prove it. The other
	// pickle suffixes are always refused by the shared name classifier.
	if source.IsPickle("", name) && strings.ToLower(path.Ext(name)) != ".bin" {
		return false
	}
	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return false
	}
	return string(magic[:]) == gguf.Magic || string(magic[:]) == "lmgg"
}
