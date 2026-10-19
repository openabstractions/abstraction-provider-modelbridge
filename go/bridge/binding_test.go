package bridge_test

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

// member is one synthetic zip entry: STORED with its data placed on a
// multiple of align (or one byte past a multiple of 32 when align is 0), or
// DEFLATE.
type member struct {
	name    string
	data    []byte
	deflate bool
	align   int64
}

func writeZip(t *testing.T, path string, members ...member) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, m := range members {
		if m.deflate {
			fw, err := w.CreateHeader(&zip.FileHeader{Name: m.name, Method: zip.Deflate})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fw.Write(m.data); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		start := int64(buf.Len()) + 30 + int64(len(m.name)) + 6
		pad := (33 - start%32) % 32
		if m.align > 0 {
			pad = (m.align - start%m.align) % m.align
		}
		extra := make([]byte, 6+pad)
		binary.LittleEndian.PutUint16(extra[0:], 0xD935)
		binary.LittleEndian.PutUint16(extra[2:], uint16(2+pad))
		h := &zip.FileHeader{Name: m.name, Method: zip.Store, CRC32: crc32.ChecksumIEEE(m.data),
			CompressedSize64: uint64(len(m.data)), UncompressedSize64: uint64(len(m.data)), Extra: extra}
		fw, err := w.CreateRaw(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(m.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// packages is an operator-named root holding one loose GGUF and one zip
// with an aligned STORED, an unaligned STORED and a DEFLATE member.
func packages(t *testing.T) (*bridge.LocalStores, string, map[string][]byte) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "packages")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	data := map[string][]byte{
		"aligned.gguf":       bytes.Repeat([]byte{7}, 70000),
		"unaligned.gguf":     bytes.Repeat([]byte{8}, 3000),
		"packed.safetensors": bytes.Repeat([]byte("bf16"), 4000),
		"loose.gguf":         []byte("loose weights"),
	}
	writeZip(t, filepath.Join(root, "pkg.zip"),
		member{name: "aligned.gguf", data: data["aligned.gguf"], align: 65536},
		member{name: "unaligned.gguf", data: data["unaligned.gguf"]},
		member{name: "packed.safetensors", data: data["packed.safetensors"], deflate: true})
	if err := os.WriteFile(filepath.Join(root, "loose.gguf"), data["loose.gguf"], 0o600); err != nil {
		t.Fatal(err)
	}
	env := source.Env{GOOS: "windows", Home: home, Getenv: func(string) string { return "" }}
	env.Config.ExtraRoots = []source.ExtraRoot{{Store: "packages", Path: root}}
	return bridge.NewLocalStores(env), root, data
}

var regionOrFile = bridge.Accepts{Kinds: []bridge.BindingKind{bridge.BindRegion, bridge.BindFile}, Alignment: 32}

func TestEveryCurrentCallerGetsAFile(t *testing.T) {
	src, root, data := packages(t)
	for _, accept := range []bridge.Accepts{bridge.FileOnly, {}, regionOrFile} {
		object, err := src.Resolve("packages", "loose.gguf", accept)
		if err != nil {
			t.Fatal(err)
		}
		want := bridge.ObjectBinding{Kind: bridge.BindFile, Path: filepath.Join(root, "loose.gguf"), Length: int64(len(data["loose.gguf"]))}
		if !reflect.DeepEqual(object.Binding, want) || object.Path != want.Path {
			t.Fatalf("accept %+v: %+v", accept, object)
		}
	}
	object, err := src.Resolve("packages", "loose.gguf", bridge.Accepts{Kinds: []bridge.BindingKind{bridge.BindRegion}})
	if err != nil || object.Binding.Kind != bridge.BindRegion || object.Binding.Offset != 0 || object.Binding.Length != int64(len(data["loose.gguf"])) {
		t.Fatalf("a region-only caller of a loose file: %+v %v", object, err)
	}
}

func TestStoredAlignedMemberIsARegion(t *testing.T) {
	src, root, data := packages(t)
	object, err := src.Resolve("packages", "pkg.zip!/aligned.gguf", regionOrFile)
	if err != nil {
		t.Fatal(err)
	}
	b := object.Binding
	if b.Kind != bridge.BindRegion || b.Path != filepath.Join(root, "pkg.zip") || b.Offset%65536 != 0 || b.Length != 70000 || object.Path != "" {
		t.Fatalf("binding %+v, object %+v", b, object)
	}
	raw, _ := os.ReadFile(b.Path)
	if !bytes.Equal(raw[b.Offset:b.Offset+b.Length], data["aligned.gguf"]) {
		t.Fatal("the region does not hold the member's bytes")
	}
	if object.File != "aligned.gguf" || object.Size != 70000 {
		t.Fatalf("object %+v", object)
	}
}

func TestAFileOnlyCallerIsRefusedAMember(t *testing.T) {
	src, _, _ := packages(t)
	object, err := src.Resolve("packages", "pkg.zip!/aligned.gguf", bridge.FileOnly)
	if !errors.Is(err, bridge.ErrUnaccepted) || object.Path != "" || object.Binding.Kind != bridge.BindRegion {
		t.Fatalf("%+v %v", object, err)
	}
}

func TestUnalignedRegionMeetsTheCallersAlignment(t *testing.T) {
	src, _, _ := packages(t)
	object, err := src.Resolve("packages", "pkg.zip!/unaligned.gguf", regionOrFile)
	if !errors.Is(err, bridge.ErrUnaccepted) || object.Binding.Kind != bridge.BindRegion || object.Binding.Offset%32 == 0 {
		t.Fatalf("an unaligned region given to a caller needing 32: %+v %v", object, err)
	}
	object, err = src.Resolve("packages", "pkg.zip!/unaligned.gguf", bridge.Accepts{Kinds: []bridge.BindingKind{bridge.BindRegion}})
	if err != nil || object.Binding.Kind != bridge.BindRegion {
		t.Fatalf("any offset: %+v %v", object, err)
	}
}

// inflates reads b's stored stream where the binding says it lies and
// checks it decodes to want with the binding's CRC-32.
func inflates(t *testing.T, b bridge.ObjectBinding, want []byte) {
	t.Helper()
	raw, err := os.ReadFile(b.Path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw[b.Offset : b.Offset+b.Packed])))
	if err != nil || !bytes.Equal(got, want) || crc32.ChecksumIEEE(got) != b.CRC32 {
		t.Fatalf("the stream at %d (%d bytes) does not decode to the member: %v", b.Offset, b.Packed, err)
	}
}

func TestDeflateMemberIsCompressed(t *testing.T) {
	src, root, data := packages(t)
	object, err := src.Resolve("packages", "pkg.zip!/packed.safetensors", regionOrFile)
	b := object.Binding
	want := bridge.ObjectBinding{Kind: bridge.BindCompressed, Path: filepath.Join(root, "pkg.zip"),
		Entries: []string{"packed.safetensors"}, Method: "deflate", Length: 16000,
		Offset: b.Offset, Packed: b.Packed, CRC32: crc32.ChecksumIEEE(data["packed.safetensors"])}
	if !errors.Is(err, bridge.ErrUnaccepted) || !reflect.DeepEqual(b, want) || b.Packed == 0 || b.Packed >= 16000 {
		t.Fatalf("%+v %v", b, err)
	}
	inflates(t, b, data["packed.safetensors"])
}

// TestNestedDeflateMemberIsLocatedInTheFile: a DEFLATE member of a STORED
// nested zip is located by its offset in the container file, so a fill
// reads its stream without reading the nested zip's directory again.
func TestNestedDeflateMemberIsLocatedInTheFile(t *testing.T) {
	src, root, data := packages(t)
	inner := filepath.Join(t.TempDir(), "inner.zip")
	writeZip(t, inner, member{name: "deep.safetensors", data: data["packed.safetensors"], deflate: true})
	innerBytes, err := os.ReadFile(inner)
	if err != nil {
		t.Fatal(err)
	}
	writeZip(t, filepath.Join(root, "outer.zip"), member{name: "inner.zip", data: innerBytes, align: 4096})
	object, err := src.Resolve("packages", "outer.zip!/inner.zip!/deep.safetensors", regionOrFile)
	b := object.Binding
	if !errors.Is(err, bridge.ErrUnaccepted) || b.Kind != bridge.BindCompressed || b.Path != filepath.Join(root, "outer.zip") ||
		!reflect.DeepEqual(b.Entries, []string{"inner.zip", "deep.safetensors"}) {
		t.Fatalf("%+v %v", b, err)
	}
	inflates(t, b, data["packed.safetensors"])
}

func TestUnknownObject(t *testing.T) {
	src, _, _ := packages(t)
	for _, id := range []string{"missing.gguf", "pkg.zip!/missing.gguf"} {
		if _, err := src.Resolve("packages", id, regionOrFile); !errors.Is(err, bridge.ErrUnknownObject) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if _, err := src.Resolve("nowhere", "loose.gguf", bridge.FileOnly); !errors.Is(err, bridge.ErrUnknownObject) {
		t.Fatalf("unknown store: %v", err)
	}
}

func TestLendRefusesAContainerMember(t *testing.T) {
	src, _, _ := packages(t)
	base := t.TempDir()
	eng := engine{name: "lmstudio", root: filepath.Join(base, "engine", bridge.LendDirectory)}
	b := bridge.New(src, open(t, filepath.Join(base, "state", "lends.json")), bridge.OSLinker(), eng)
	for _, copy := range []bool{false, true} {
		got := b.Lend("packages", "pkg.zip!/aligned.gguf", "lmstudio", copy)
		if got.Outcome != wire.LendOutcomeLinkRefused || got.Lend != nil {
			t.Fatalf("copy %v: %+v", copy, got)
		}
	}
	if _, err := os.Stat(eng.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused lend wrote into the engine: %v", err)
	}
	if got := b.Lend("packages", "loose.gguf", "lmstudio", false); got.Outcome != wire.LendOutcomeLent {
		t.Fatalf("a loose file still lends: %+v", got)
	}
}
