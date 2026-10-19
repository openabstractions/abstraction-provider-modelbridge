package bridge

import (
	"errors"
	"fmt"
	"slices"
)

// BindingKind names how an object's bytes reach an engine on this machine
// (research/model-locators/DECISION.md §7 rule 2, MODELBRIDGE-L19).
type BindingKind string

const (
	// BindFile is a whole file at Binding.Path.
	BindFile BindingKind = "file"
	// BindRegion is Binding.Length bytes at Binding.Offset of the file at
	// Binding.Path: a STORED, unencrypted zip member inside STORED zips only.
	BindRegion BindingKind = "region"
	// BindCompressed is a member that must be inflated before any engine
	// reads it: the container file at Binding.Path, the member names from
	// the container inward in Binding.Entries, and its Binding.Method.
	BindCompressed BindingKind = "compressed"
	// BindMemory is bytes placed in a read-only memory section the provider
	// host owns and lends by handle (research/model-locators/MEMORY-LENDING.md;
	// inference CONTRACT.md INF-MS1..INF-MS6): Binding.Handle, Length,
	// Digest, ChunkDigests, and in From the file, region or compressed
	// binding it was filled from. Only the provider kit's fill produces it;
	// no Source answers it.
	BindMemory BindingKind = "memory"
	// BindDevice is reserved for bytes resident on a device, as VISION
	// 2026-09-15 reserved it. Nothing produces it.
	BindDevice BindingKind = "device"
)

// ObjectBinding is the local projection of one object's locator. It is
// provider-side data: it never reaches an application or a contract field.
type ObjectBinding struct {
	Kind BindingKind
	// Path is the file for BindFile, and the container file for
	// BindRegion and BindCompressed.
	Path string
	// Offset is where a region starts in Path, and where a compressed
	// member's stored stream starts in Path.
	Offset int64
	// Length is the object's uncompressed size, for every kind.
	Length int64
	// Entries names a compressed member from the container inward.
	Entries []string
	// Method is a compressed member's compression method, "deflate".
	Method string
	// Packed is a compressed member's stored size, and CRC32 the CRC-32
	// of its uncompressed bytes the stream is checked against.
	Packed int64
	CRC32  uint32
	// Handle is a memory binding's section handle in the provider host's
	// own process; an engine receives a read-only duplicate at launch.
	Handle uintptr
	// Digest is a memory binding's "sha256:" digest its bytes were proved
	// against, and ChunkDigests the hex SHA-256 of each 16 MiB span.
	Digest       string
	ChunkDigests []string
	// From is the binding a memory binding was filled from.
	From *ObjectBinding
}

// Accepts is what one caller's engine reads, as its pinned version reads it
// (DECISION.md §7 rule 3). Alignment is the boundary a region's offset must
// sit on; zero or one accepts any offset. An Accepts naming no kind accepts a
// file only.
type Accepts struct {
	Kinds     []BindingKind
	Alignment int64
}

// FileOnly is what every engine OA launches today reads: a path.
var FileOnly = Accepts{Kinds: []BindingKind{BindFile}}

func (a Accepts) has(k BindingKind) bool {
	if len(a.Kinds) == 0 {
		return k == BindFile
	}
	return slices.Contains(a.Kinds, k)
}

func (a Accepts) aligned(offset int64) bool {
	return a.Alignment <= 1 || offset%a.Alignment == 0
}

var (
	// ErrUnknownObject: no store this source reads holds the id.
	ErrUnknownObject = errors.New("unknown object")
	// ErrUnaccepted: the object exists, and none of the kinds the caller
	// accepts reaches it without materialization, which is not built. The
	// returned Object then carries the object's own binding.
	ErrUnaccepted = errors.New("no accepted binding")
	// ErrPickle: the object is a PyTorch pickle, which OA never loads; no
	// caller receives a binding to one (MODELBRIDGE-L20).
	ErrPickle = errors.New("pickle")
)

// choose picks the first kind the caller accepts in the order region, file,
// memory (DECISION.md §7 rule 3) from what the object natively is. A loose
// file is also a region from offset zero. A region that misses the caller's
// alignment, and every compressed member, is ErrUnaccepted.
func choose(native ObjectBinding, accept Accepts) (ObjectBinding, error) {
	switch native.Kind {
	case BindFile:
		if accept.has(BindFile) {
			return native, nil
		}
		if accept.has(BindRegion) {
			return ObjectBinding{Kind: BindRegion, Path: native.Path, Length: native.Length}, nil
		}
	case BindRegion:
		if accept.has(BindRegion) && accept.aligned(native.Offset) {
			return native, nil
		}
		return native, fmt.Errorf("%w: the object is %d bytes at offset %d inside %s, and this engine reads %s",
			ErrUnaccepted, native.Length, native.Offset, native.Path, accept.describe())
	case BindCompressed:
		return native, fmt.Errorf("%w: the object is a %s-compressed member of %s, which must be unpacked before an engine reads it",
			ErrUnaccepted, native.Method, native.Path)
	}
	return native, fmt.Errorf("%w: the object is a %s binding, and this engine reads %s", ErrUnaccepted, native.Kind, accept.describe())
}

func (a Accepts) describe() string {
	kinds := a.Kinds
	if len(kinds) == 0 {
		kinds = FileOnly.Kinds
	}
	out := ""
	for i, k := range kinds {
		if i > 0 {
			out += " or "
		}
		out += string(k)
		if k == BindRegion && a.Alignment > 1 {
			out += fmt.Sprintf(" starting on a multiple of %d", a.Alignment)
		}
	}
	return out
}
