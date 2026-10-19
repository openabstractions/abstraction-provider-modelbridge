package bridge_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

type descriptorStore struct {
	store    content.Store
	manifest content.Manifest
	stray    bool
}

func (a descriptorStore) Program() string { return "jan" }
func (a descriptorStore) Discover(source.Env) source.Discovery {
	return source.Discovery{Stores: []content.Store{a.store}}
}
func (a descriptorStore) Observe(s content.Store, _ source.Env) source.StoreObservation {
	if a.stray {
		return source.StoreObservation{Store: s, Objects: []content.Object{{Locator: "model.gguf", Size: 7}}}
	}
	return source.StoreObservation{Store: s, Manifests: []content.ManifestHolders{{Manifest: a.manifest}}}
}

func descriptorFixture(t *testing.T, family string) (*bridge.LocalStores, engine, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "store")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "model.gguf"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := content.Manifest{ID: "manifest-1", Store: "jan", Kind: "abstraction.model/descriptor@1",
		Descriptor: map[string]string{"family": family},
		Names:      []content.Name{{Scheme: "jan", Name: "publisher/other"}},
		Entries:    []content.Entry{{Role: source.RoleWeights, Locator: "model.gguf", Size: 7}}}
	store := content.Store{Name: "jan", Program: "jan", Locator: root, Present: true}
	src := &bridge.LocalStores{Adapters: []source.Adapter{descriptorStore{store: store, manifest: manifest}}}
	eng := engine{name: "lmstudio", root: filepath.Join(base, "engine", bridge.LendDirectory)}
	return src, eng, filepath.Join(base, "state", "lends.json")
}

func TestLocalStoresUsesDescriptorFamilyForGenericLend(t *testing.T) {
	src, eng, ledgerPath := descriptorFixture(t, "canonical-family")
	object, ok := resolve(src, "jan", "manifest-1")
	if !ok || object.Family != "canonical-family" {
		t.Fatalf("resolved descriptor family: %+v, %v", object, ok)
	}
	b := bridge.New(src, open(t, ledgerPath), bridge.OSLinker(), eng)
	got := b.Lend("jan", "manifest-1", "lmstudio", false)
	if got.Outcome != wire.LendOutcomeLent || got.Lend == nil {
		t.Fatalf("lend: %+v", got)
	}
	want := filepath.Join(eng.root, "canonical-family", "canonical-family.gguf")
	if got.Lend.Link != want {
		t.Fatalf("link %q, want %q", got.Lend.Link, want)
	}
}

func TestLocalStoresFamilyFallbackAndHostileDescriptor(t *testing.T) {
	for _, tc := range []struct {
		name, family, want string
		ok                 bool
	}{
		{name: "absent", family: "", want: "other", ok: true},
		{name: "parent", family: "../escape", ok: false},
		{name: "separator", family: `outside\escape`, ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, _, _ := descriptorFixture(t, tc.family)
			object, ok := resolve(src, "jan", "manifest-1")
			if ok != tc.ok || (ok && object.Family != tc.want) {
				t.Fatalf("resolved family: %+v, %v; want %q, %v", object, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestLocalStoresDoesNotApplyDescriptorFamilyToOtherKindsOrStrayObjects(t *testing.T) {
	src, _, _ := descriptorFixture(t, "canonical-family")
	a := src.Adapters[0].(descriptorStore)
	a.manifest.Kind = "abstraction.storage/object@1"
	src.Adapters[0] = a
	if object, ok := resolve(src, "jan", "manifest-1"); !ok || object.Family != "other" {
		t.Fatalf("non-model manifest fallback: %+v, %v", object, ok)
	}
	a.stray = true
	src.Adapters[0] = a
	if object, ok := resolve(src, "jan", "model.gguf"); !ok || object.Family != "model" {
		t.Fatalf("stray object fallback: %+v, %v", object, ok)
	}
}
