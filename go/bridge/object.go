// Package bridge lends a model file one store holds to an engine installed on
// the same machine, by link, and keeps the ledger that says which lends are in
// force. It never moves, renames or deletes an original. Of a model's bytes it
// reads only the first four of a file whose name states no model format, to
// tell GGUF from a pickle (MODELBRIDGE-L16, MODELBRIDGE-L21).
package bridge

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/container"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// Object is one weight file, named the way the storage inventory names it and
// resolved to the path only this provider reads.
type Object struct {
	// Store is the inventory's store name; ID is the manifest id the source
	// published for it.
	Store, ID string
	// Locator is the object's locator inside its store, as the source spells
	// it: provider binding data, never an application's authority.
	Locator string
	// Path is the absolute path on this machine when Binding is a file, and
	// empty for every other kind.
	Path string
	// Binding is how the object's bytes reach an engine: the kind the
	// caller accepted, or, with ErrUnaccepted, the object's own kind.
	Binding ObjectBinding
	// Family is the descriptor's identity key when present, otherwise the
	// name a lend derives for the engine's directory from the file or store.
	Family string
	// File is the base name of the file.
	File string
	Size int64
	// Kind is the ComfyUI folder-name classification (MODELBRIDGE-L18): the leading
	// path segment of the object's own name inside a native ComfyUI store,
	// where ComfyUI itself filed it under checkpoints, loras, vae and the
	// rest. Empty where no store classifies the object this way; a lend to
	// an engine that keeps one folder per kind then refuses rather than
	// guessing one.
	Kind string
}

// Source resolves the inventory's naming to a binding on this machine. The
// bridge asks a source for one; the catalogue carries none.
type Source interface {
	// Resolve finds the object id names in store and binds it in a kind
	// accept names (MODELBRIDGE-L19). id is a manifest id, the name the
	// store gives that manifest, or an object locator. Every Source MUST
	// bind only an object in a model format (MODELBRIDGE-L21) and MUST
	// answer ErrPickle for a pickle, for every caller and every kind
	// (MODELBRIDGE-L20). An id no store holds, and an object in no model
	// format, is ErrUnknownObject; an object no accepted kind reaches is
	// ErrUnaccepted, with the object and its own binding returned.
	Resolve(store, id string, accept Accepts) (Object, error)
	// Stores is every store this source discovered, in report order.
	Stores() []string
	// Root is the locator of one store, empty when it has none.
	Root(store string) string
}

// LocalStores reads the same machine abstraction-storage-over-local-stores
// reads, through its own adapters. It hashes nothing and writes nothing.
type LocalStores struct {
	Env      source.Env
	Adapters []source.Adapter
}

// NewLocalStores reads env with every adapter in report order.
func NewLocalStores(env source.Env) *LocalStores {
	return &LocalStores{Env: env, Adapters: snapshot.Adapters()}
}

func (l *LocalStores) take() snapshot.Result { return snapshot.Take(l.Env, l.Adapters) }

func (l *LocalStores) Stores() []string {
	result := l.take()
	out := make([]string, 0, len(result.Page.Stores))
	for _, s := range result.Page.Stores {
		out = append(out, s.Name)
	}
	return out
}

func (l *LocalStores) Root(store string) string {
	for _, s := range l.take().Page.Stores {
		if s.Name == store {
			return s.Locator
		}
	}
	return ""
}

// Resolve matches id against the manifest id, each name the store gives the
// manifest, and each entry's locator, in that order. The first weights entry
// of the matched manifest is the object; a stray object matches by locator.
func (l *LocalStores) Resolve(store, id string, accept Accepts) (Object, error) {
	result := l.take()
	root := ""
	for _, s := range result.Page.Stores {
		if s.Name == store {
			root = s.Locator
		}
	}
	if root == "" {
		return Object{}, fmt.Errorf("%w: no store %s", ErrUnknownObject, store)
	}
	for _, m := range result.Page.Manifests {
		if m.Manifest.Store != store || !manifestNamed(m.Manifest, id) {
			continue
		}
		entry, ok := weights(m.Manifest)
		if !ok {
			continue
		}
		name := ""
		if len(m.Manifest.Names) > 0 {
			name = m.Manifest.Names[0].Name
		}
		family := ""
		if m.Manifest.Kind == "abstraction.model/descriptor@1" {
			family = m.Manifest.Descriptor["family"]
		}
		return object(store, m.Manifest.ID, entry.Locator, root, name, family, entry.Size, accept)
	}
	for _, o := range result.Page.Objects {
		if o.Store == store && o.Object.Locator == id && !o.Object.Partial {
			return object(store, "", o.Object.Locator, root, "", "", o.Object.Size, accept)
		}
	}
	return Object{}, fmt.Errorf("%w: no object %s in store %s", ErrUnknownObject, id, store)
}

func object(store, id, locator, root, name, descriptorFamily string, size int64, accept Accepts) (Object, error) {
	base := path.Base(locator)
	family := FamilyOf(name, base)
	if descriptorFamily != "" {
		// A descriptor is authoritative, but its key is still untrusted path
		// data when used as one directory beneath an engine's lend root.
		if !familyComponent(descriptorFamily) {
			return Object{}, fmt.Errorf("%w: its descriptor family is no directory name", ErrUnknownObject)
		}
		family = descriptorFamily
	}
	native, err := nativeBinding(root, locator, size)
	if err != nil {
		return Object{}, err
	}
	// Resolve binds only an object in a model format (MODELBRIDGE-L21); a
	// pickle is the named case of that (MODELBRIDGE-L20, storage LS-1). A
	// loose file without a format extension is judged by its first four
	// bytes; a container member by its name alone (LS-9), which an empty
	// path gives the source's classifiers.
	header := ""
	if native.Kind == BindFile {
		header = native.Path
	}
	if source.IsPickle(header, base) {
		return Object{}, fmt.Errorf("%w: %s in store %s is a pickle, which OA never loads", ErrPickle, locator, store)
	}
	if source.ModelFormatOf(header, base) == "" {
		return Object{}, fmt.Errorf("%w: %s in store %s is in no model format OA loads", ErrUnknownObject, locator, store)
	}
	o := Object{Store: store, ID: id, Locator: locator, Family: family, File: base, Size: size, Kind: KindOf(store, locator)}
	o.Binding, err = choose(native, accept)
	if o.Binding.Kind == BindFile && err == nil {
		o.Path = o.Binding.Path
	}
	return o, err
}

// nativeBinding is what the object's locator names on this machine: a file
// under the store's root, or a member of a zip there (LS-8), which is a
// region when every layer around its bytes is STORED and compressed
// otherwise. The zip's directory is read again on every call, so a binding
// always describes the container as it is now.
func nativeBinding(root, locator string, size int64) (ObjectBinding, error) {
	file, chain, ok := container.SplitMemberLocator(locator)
	if ok {
		zipPath := filepath.Join(root, filepath.FromSlash(file))
		if st, err := os.Stat(zipPath); err == nil && st.Mode().IsRegular() {
			members, err := container.Members(zipPath)
			if err != nil {
				return ObjectBinding{}, fmt.Errorf("%w: %v", ErrUnknownObject, err)
			}
			for _, m := range members {
				if !slices.Equal(m.Chain(), chain) {
					continue
				}
				if m.InPlace {
					return ObjectBinding{Kind: BindRegion, Path: zipPath, Offset: m.Offset, Length: m.Size}, nil
				}
				// Members lists only members inside STORED zips, so each
				// DEFLATE stream has a place in the file (LS-8).
				return ObjectBinding{Kind: BindCompressed, Path: zipPath, Entries: chain, Method: "deflate",
					Offset: m.FileOffset, Packed: m.Packed, CRC32: m.CRC32, Length: m.Size}, nil
			}
			return ObjectBinding{}, fmt.Errorf("%w: %s holds no member %s", ErrUnknownObject, file, strings.Join(chain, "!/"))
		}
	}
	return ObjectBinding{Kind: BindFile, Path: filepath.Join(root, filepath.FromSlash(locator)), Length: size}, nil
}

func familyComponent(name string) bool {
	return filepath.IsLocal(name) && filepath.Base(name) == name &&
		!strings.ContainsAny(name, "/\\:") && strings.TrimRight(name, ". ") == name &&
		strings.IndexFunc(name, unicode.IsControl) < 0
}

// comfyuiStore reports whether store is a native ComfyUI installation's own
// models tree, named the way comfyui.go names one: "comfyui", or "comfyui#2"
// and onward for a second installation. A comfyui-extra claim, which points
// at another program's own folder layout (an A1111 tree, say), is not: its
// relative names carry that program's own layout, not ComfyUI's kind
// folders, so KindOf names no kind for one.
func comfyuiStore(store string) bool {
	return store == "comfyui" || strings.HasPrefix(store, "comfyui#")
}

// KindOf is the ComfyUI folder-kind an object carries (MODELBRIDGE-L18): the leading
// path segment of its own name inside a native ComfyUI store, lowercased,
// where that segment is what ComfyUI itself filed the object under —
// checkpoints/sd15.safetensors lends under "checkpoints". Every other store
// publishes no such classification today, so KindOf names none for it rather
// than guessing one from a file's own name.
func KindOf(store, locator string) string {
	if !comfyuiStore(store) {
		return ""
	}
	rel := strings.TrimPrefix(filepath.ToSlash(locator), "/")
	i := strings.Index(rel, "/")
	if i <= 0 {
		return ""
	}
	return strings.ToLower(rel[:i])
}

// manifestNamed reports whether id names m: its id, one of its names, or one
// of its entries' locators.
func manifestNamed(m content.Manifest, id string) bool {
	if id == "" {
		return false
	}
	if m.ID == id {
		return true
	}
	for _, n := range m.Names {
		if n.Name == id {
			return true
		}
	}
	for _, e := range m.Entries {
		if e.Locator == id {
			return true
		}
	}
	return false
}

// weights is the manifest's weights entry, the file a lend links.
func weights(m content.Manifest) (content.Entry, bool) {
	for _, e := range m.Entries {
		if e.Role == source.RoleWeights && e.Locator != "" {
			return e, true
		}
	}
	// A manifest of a projector or a VAE alone has that component as its
	// weights; a manifest of configuration, templates or other files has
	// none (MODELBRIDGE-L21).
	for _, e := range m.Entries {
		if (e.Role == source.RoleProjector || e.Role == source.RoleVAE) && e.Locator != "" {
			return e, true
		}
	}
	return content.Entry{}, false
}

// quant is the trailing quantisation token engines append to a weight file's
// name. LM Studio derives the model name from the directory a file sits in, so
// the directory is the family without that token.
var quant = regexp.MustCompile(`(?i)[-._](iq\d+[a-z0-9_]*|q\d+[a-z0-9_]*|bf16|fp16|fp32|f16|f32|fp8[a-z0-9_]*|int[48]|mxfp\d+|\d+bit)$`)

// generic is a file name that says nothing about which model it holds. Jan
// stores every model as model.gguf in a directory named for the model, so the
// store's own name for the manifest is the only thing worth lending under.
func generic(name string) bool {
	return name == "" || name == "model" || name == "weights" || name == "ggml-model"
}

// FamilyOf is the directory name a lend uses: the file's own name where it
// says which model this is, and otherwise the deepest segment of the store's
// own name for the manifest that does. The engine derives its model name from
// this directory (the 2026-09-22 experiment measured LM Studio doing exactly
// that), so a directory called "model" is no name at all.
func FamilyOf(name, file string) string {
	if f := Family(file); !generic(f) {
		return f
	}
	segments := strings.Split(strings.Trim(strings.ReplaceAll(name, "\\", "/"), "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		if f := Family(segments[i]); !generic(f) {
			return f
		}
	}
	return Family(file)
}

// Family is one name reduced to a directory name: without its extension,
// without a trailing quantisation token, lowercased.
func Family(file string) string {
	name := strings.TrimSuffix(file, path.Ext(file))
	for {
		trimmed := quant.ReplaceAllString(name, "")
		if trimmed == name {
			break
		}
		name = trimmed
	}
	name = strings.ToLower(strings.Trim(name, "-._ "))
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			return r
		}
		return '-'
	}, name)
	if name == "" {
		return "model"
	}
	return name
}
