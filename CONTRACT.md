# provider-modelbridge contract

Storage's [`lend.thrift`](https://github.com/openabstractions/abstraction-storage/blob/main/lend.thrift) defines `abstraction.storage/lend@1`, the
lending service: it places a model file one store on this machine already
holds into an engine installed here, by link, so that engine serves it. It
writes exactly one link and the directories above it, it records what it
wrote, and `Unlend` removes that and nothing else. This file states the
obligations a provider meets; [README.md](README.md) holds the walkthrough.
`research/model-bridge/DECISION.md` rules 2, 3 and 5 in the parent project
carry the design; what was measured on a real machine moved to
[HISTORY.md](HISTORY.md).

Binds: [lend.thrift](https://github.com/openabstractions/abstraction-storage/blob/main/lend.thrift)

## Reading this page

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this
document are to be interpreted as described in BCP 14 [RFC2119] [RFC8174]
when, and only when, they appear in all capitals, as shown here.

The rule ids below, such as `MODELBRIDGE-L1`, are what tests and refusals
cite. A retired id is never reused; [HISTORY.md](HISTORY.md) keeps it with
the release it left.

| Letter | Meaning |
| --- | --- |
| L | lending |

| Prose | Wire (until its own release) |
| --- | --- |
| resource `server:<name>` | resource `engine:<name>`, owned by rights' stored policy; the dual reader lands with that module, not here (`research/vocabulary/RENAME-PLAN.md` §4, first release) |
| `link_type` | `Lend.kind` (the `Link` enum field), pending `research/vocabulary/RENAME-PLAN.md` §4's third release |

## Naming

**[MODELBRIDGE-L1] Object naming.** The service MUST name an object the way
the storage inventory names it: a store and an id, never a path. `Lend`
takes `store` and `object`. `object` is a manifest id, the name that store
gives that manifest, or an object locator, as `abstraction.storage/inventory@1`
reports them. The service resolves the id to a binding on this machine
through a source (MODELBRIDGE-L19); the catalogue carries no path and no
caller supplies one. An id no store the service reads holds is
`unknown_object`. The ledger records the manifest id where the source
published one, and the locator otherwise.

**[MODELBRIDGE-L19] An object resolves to a binding with a kind.** The
source's `Resolve` MUST answer a store and an id with a binding of one of
these kinds, never with a bare path
(`research/model-locators/DECISION.md` §7 rule 2, VISION 2026-10-01):
`file` (a path), `region` (a path, an offset and a length: a STORED,
unencrypted zip member inside STORED zips only), `compressed` (the
container, the member names from the container inward, the method, and its
stored stream's offset in the container file, stored size and CRC-32),
`memory` and `device`. No source answers `memory`: the inference provider
kit's fill produces it from a `file`, `region` or `compressed` binding
(inference CONTRACT.md INF-MS1..INF-MS6), with the section's handle in the
provider host, its length, digest and chunk digests, and the binding it was
filled from. Nothing produces `device` yet. Each
caller MUST declare the kinds its engine reads, and a region's alignment;
the source MUST answer only a declared kind, choosing in the order
`region`, `file`, `memory`, and MUST refuse an object no declared kind
reaches without materialization. A loose file is a `file`, and a `region`
from offset zero to a caller reading regions only. The source MUST re-read a
container's central directory on every resolution and MUST NOT read or
hash a member's bytes to bind it. A binding is provider-side data: it
never reaches an application, the ledger or a contract field. `Lend`
declares `file` only, because a link names a whole file; a member of a
container is `link_refused`, with or without `copy`, until materialization
is built. The model host declares `file` only for `llama-server` at its pinned
release (abstraction-provider-modelhost CONTRACT.md MODELHOST-L1).

**[MODELBRIDGE-L2] Engine naming.** An engine is a registered model server's
name. `lmstudio`, `lemonade` and `comfyui` are the engines this service
names. An engine the service does not lend to, and an engine whose own
configuration cannot be pointed at the lent directory without a person
editing its configuration file, are both `unsupported_engine`, with the
reason in `detail`.

**[MODELBRIDGE-L20] A pickle is never bound.** OA never loads a pickle
(VISION 2026-10-01, the third-party review rule). Every source's `Resolve`
MUST refuse, for every caller and every accepted kind, an object the
storage reader's pickle rule classifies as one
(storage-over-local-stores CONTRACT.md LS-1: a file named `.pth`, `.pt`,
`.ckpt`, `.pkl` or `.pickle`, or a weight `.bin` whose header is neither
GGUF's magic nor whisper.cpp's ggml magic `lmgg`; a container member by its
name alone, LS-9), whether the caller names it by locator, by
manifest id or by name. To classify a loose `.bin` the source reads at most
its first four bytes. `Lend` answers that refusal `not_permitted` with a
`detail` that opens with the reason `object:pickle`, with or without `copy`,
and writes nothing. The model host answers it `unavailable` with reason
`object:pickle:<store>/<id>` before any lease
(abstraction-provider-modelhost CONTRACT.md MODELHOST-L1).

**[MODELBRIDGE-L21] Only a model format is bound.** The source's `Resolve`
MUST bind only an object in a model format OA loads: GGUF (`.gguf`, or any
file whose first four bytes are GGUF's magic, such as an Ollama blob or a
GGUF `.bin`), whisper.cpp's ggml (any file whose first four bytes are
`lmgg`, such as `ggml-medium.bin`), safetensors (`.safetensors`, `.sft`),
ONNX (`.onnx`) and FastFlowLM's `.q4nx`. A container member is judged by its name alone
(storage-over-local-stores CONTRACT.md LS-9), so a GGUF `.bin` member is
refused as a pickle. A manifest's object is its entry with role `weights`,
or failing one, its entry with role `projector` or `vae`; a manifest with
none of these (a Hugging Face snapshot of configuration files or a `.pkl`,
say) names no object. An object in no model format, and a manifest naming
none, is `unknown_object`; a pickle is MODELBRIDGE-L20's refusal. To judge a
name that states no format the source reads at most four bytes, from a
regular file only: it opens without waiting (non-blocking on Unix, where a
FIFO's open would wait for a writer while `Lend` holds its lock) and checks
the opened handle, and a FIFO, device or directory is in no model format.

## The link

**[MODELBRIDGE-L3] A lend is a link.** The service MUST NOT move, rename or
delete the source file. The symbolic link is the default. The hard link is
the fallback, taken when the operating system refuses this process the
symbolic-link privilege, and the source and the engine's directory share a
volume. Windows without Developer Mode refuses the privilege to the process,
independent of which engine is the target; that is the common case, and the
service falls back without asking. A link neither kind of which can be
placed is `link_refused`.

**[MODELBRIDGE-L4] Copy on request.** A copy MUST be made only when the
caller asks for one, and MUST be counted as a copy. `Lend`'s `copy` argument
is that request. A copy carries `link_type` `copy` on its ledger entry, so no
reader mistakes a copy for a link. Without it, a source and an engine
directory on different volumes read `link_refused`.

**[MODELBRIDGE-L5] Refusal leaves nothing behind.** The service MUST remove
every directory the attempt created before the refusal is returned, and MUST
NOT write a committed entry to the ledger for a refused lend. If a crash or
filesystem change makes ownership uncertain, the service MUST retain its
pending intent and refuse a retry of that lend until an operator reconciles
the artifact; it MUST NOT delete an unproven file to satisfy cleanup.

**[MODELBRIDGE-L6] Naming the link.** The link MUST keep the source file's
own name, unless that name says nothing. The engine derives its own model
name from what it is given. A store that names every weight file the same —
Jan writes each model as `model.gguf` — lends under the family name instead
(`abstraction.model/descriptor@1`'s `family`, MODEL-C2).

**[MODELBRIDGE-L7] One directory per lend.** Every lend MUST sit under one
directory this service owns inside the engine's own directory,
`openabstractions-lend`, and under one directory per family inside that. A
borrowed model is visibly borrowed in the engine's own list: LM Studio reads
the top level as the publisher.

**[MODELBRIDGE-L18] ComfyUI's folders.** ComfyUI keeps one folder under its
`models` directory per kind it serves (`checkpoints`, `loras`, `vae`,
`clip`, `unet`, `diffusion_models`, `controlnet`, `embeddings`,
`upscale_models`, `clip_vision`, `text_encoders`), a subset of ComfyUI's own
folder list, unlike LM Studio and Lemonade, which take every servable file
under the one root MODELBRIDGE-L7 already states regardless of kind. The
service MUST place a lend to ComfyUI under `openabstractions-lend` inside
the folder for the object's own kind, MODELBRIDGE-L7's shape repeated per
folder rather than once. The kind is read from the object's own name inside
a native ComfyUI store, the leading path segment ComfyUI itself filed it
under; an object with no such reading, and one whose kind ComfyUI keeps no
folder for among those named above, are each `unsupported_engine`, the
outcome MODELBRIDGE-L2 already carries for an engine a file cannot be placed
with. ComfyUI is never pointed at a lend through `extra_model_paths.yaml`:
that file is ComfyUI's own, read once at its own startup, so a root named
there would need a restart, and writing it would be editing a file ComfyUI
owns.

## The ledger

**[MODELBRIDGE-L8] The ledger, not the engine's list, is authoritative.**
`Lends` is the ledger, oldest first. The engine's own list may keep naming a
model after `Unlend` until that engine's idle-unload timer fires — LM
Studio's `jitModelTTL` was up to 3600 s on the measured machine; the value is
the person's, set in LM Studio's own settings, and its listing lagged the
removal in the measurement recorded in [HISTORY.md](HISTORY.md). A caller
that asks the engine whether a model is still available right after `Unlend`
can get a false yes; the ledger cannot.

**[MODELBRIDGE-L9] Write before reply.** The service MUST write an entry
before the caller is told. The ledger is replaced in one rename, so a reader
sees a whole ledger or the one before it, and it survives a restart of the
service. A ledger that cannot be written undoes the link and refuses
`unavailable`. Before placement, the service records an intent and uses a
unique staging name. It records the placed file's native identity before
reserving the final name. A restart completes a placed intent or removes a
provably owned staged file. An ambiguous stage remains for reconciliation;
it never becomes an `already_lent` result without an entry.

**[MODELBRIDGE-L10] One lend per engine.** The service MUST refuse a second
`Lend` of an object already in force at that engine as `already_lent`,
carrying the entry that holds it. A file already standing at the link's own
path, which no entry names, is `link_refused`; the service MUST NOT overwrite
anything in another program's directory or claim an unrecorded file as a lend.

**[MODELBRIDGE-L11] What `Unlend` removes.** `Unlend` MUST remove exactly the
link this entry created and the directories this service made for it,
upwards to and including `openabstractions-lend`, stopping at the first
directory that still holds something. The source file MUST stay untouched.
The entry leaves the ledger. An entry no ledger row names, including one
already removed, is `unknown`. The provider records native file and parent
directory identities and checks them before undo. It moves a candidate to a
recoverable quarantine name before deleting it. If the path was replaced, it
preserves the replacement, returns `unavailable`, and retains the entry for
reconciliation. It prunes only directories whose creation it recorded. A
legacy ledger row with no ownership record also returns `unavailable` until
an operator reconciles it.

## The right

**[MODELBRIDGE-L12] Lending is gated by a rule.** Lending is gated by
`abstraction.storage/lend` on resource `server:<name>`. Writing into another
application's directory is an action on a resource the person permits once,
through the runtime's first-use question, like every other program right.
The service MUST refuse the first `Lend` by a program with no rule as
`not_permitted` and put the question where the person answers it; the `Lend`
after the answer goes through. Discovery needs no permission.

**[MODELBRIDGE-L13] `Unlend`'s right.** `Unlend` is decided on the entry's
own engine. A lend is taken back under the right that placed it.

**[MODELBRIDGE-L14] The peer check.** Applications never reach this service
directly. Their calls travel through the runtime, which binds the caller
from the connection's program proof, decides the rule, and forwards. The
provider listens on the shared identity-bound transport and admits only the
runtime executable beside its installed program, including the windowless
runtime on Windows. `--runtime-program` names an explicit executable for a
custom installation. A call whose peer identity could not be obtained fails
with the error code `caller_unavailable`, one below Program proof fails with
`identity_required`, and a bound peer from another account or program reads
`not_permitted`. Divergences records this release's gap against
`abstraction-provider-modelhost`'s own peer check.

## Outcomes

| Call | Outcome | Meaning |
| --- | --- | --- |
| `Lend` | `lent` | The link was placed and the ledger holds the entry. |
| `Lend` | `not_permitted` | No rule lets the calling program lend to this engine (MODELBRIDGE-L12). |
| `Lend` | `not_permitted`, `detail` `object:pickle: …` | The object is a PyTorch pickle, which OA never loads or lends (MODELBRIDGE-L20). |
| `Lend` | `link_refused` | The object is a member of a container, which a link cannot point into (MODELBRIDGE-L19). |
| `Lend` | `unsupported_engine` | The engine is not one this service lends to, or its configuration cannot be pointed at the lent directory without a person editing it (MODELBRIDGE-L2, MODELBRIDGE-L18). |
| `Lend` | `link_refused` | Neither a symbolic nor a hard link could be placed (MODELBRIDGE-L3). |
| `Lend` | `already_lent` | The object is already in force at that engine; the entry that holds it is returned (MODELBRIDGE-L10). |
| `Lend` | `unknown_object` | No store this service reads holds the id, or what it names is in no model format OA loads (MODELBRIDGE-L1, MODELBRIDGE-L21). |
| `Lend` | `invalid` | A `store`, `object` or `engine` argument is empty or over its bound (Bounds). |
| `Lend` | `unavailable` | The ledger could not be written, a staged artifact needs reconciliation, or the peer identity was not established (MODELBRIDGE-L5, MODELBRIDGE-L9, MODELBRIDGE-L14). |
| `Unlend` | `unlent` | The link and every directory this lend created are gone, the entry has left the ledger (MODELBRIDGE-L11). |
| `Unlend` | `not_permitted` | No rule lets the calling program remove this entry's lend (MODELBRIDGE-L13). |
| `Unlend` | `unknown` | No ledger row names this entry (MODELBRIDGE-L11). |
| `Unlend` | `invalid` | The `entry` argument is empty or over its bound (Bounds). |
| `Unlend` | `unavailable` | The ledger could not be written, ownership could not be proved, or the peer identity was not established. |
| `Lends` | `page` | One page of the ledger, oldest first. |
| `Lends` | `not_permitted` | The calling program holds no operator right for the ledger, or the peer is a different account (MODELBRIDGE-L17, MODELBRIDGE-L14). |
| `Lends` | `invalid` | `limit` or `continuation` is out of bounds (Bounds). |
| `Lends` | `unavailable` | The peer identity was not established. |

## Bounds

**[MODELBRIDGE-L15] Sizes.** `Lends`' `limit` MUST be 1..256; a continuation
MUST be at most 256 bytes and name a retained page position, and an unknown
one is `invalid`. The ledger holds at most 1024 entries. A `store`, `object`
or `engine` that is empty or longer than 256, 1024 and 64 bytes is `invalid`.

**[MODELBRIDGE-L16] No content identity by bytes.** For a link, the service
MUST NOT read model bytes beyond the four header bytes MODELBRIDGE-L20 and
MODELBRIDGE-L21 read to classify a file whose name states no model format. A requested copy reads those bytes to write the
copy (MODELBRIDGE-L4). The service MUST NOT hash model contents. It may hash
its own small path string to name a recovery quarantine.

**[MODELBRIDGE-L17] The ledger is an operator record.** Like the registry's
declarations, the ledger is for the operator tools that placed the link and
must be able to remove exactly it: the entry names the link's absolute path
and the engine because "which tool breaks if this moves" is the question an
audit asks. Applications never read the ledger; they see a lent model the
way they see any other, as a model server's model in the router's list,
with the store it came from in `held_in`. The path in an entry is therefore
not a binding an application can branch on, and `Lends` is gated by the
operator right the runtime writes for its own operator programs, never
granted to an application by a first-use question.

## Divergences

- **MODELBRIDGE-L14 against `not_permitted` and `forbidden`.** The
  boundary's own peer check — a caller of a different account — and a
  verified caller's missing lend rule both read `not_permitted` this
  release. `abstraction-provider-modelhost`'s own peer check
  (MODELHOST-R3) already reads `forbidden` for the same distinction. Closing
  the gap needs `forbidden` added to `LendOutcome`, `UnlendOutcome` and
  `LendsOutcome`, scheduled for `research/vocabulary/RENAME-PLAN.md` §4's
  third release (providers review F1).

## Not built

Materialization of a `region` or `compressed` binding into a counted
`file` (a block clone, or one copy in the `cache` storage class) and `lend@1`'s
`clone` link are `research/model-locators/DECISION.md` §9 step 3; until they
land, a container member reaches no engine (MODELBRIDGE-L19).
`Lend` names no engine process: it neither starts nor stops one. Spawning an
engine for a file no installed engine can serve is DECISION.md rule 4's
third step and belongs to a later slice. MODELBRIDGE-L18 classifies a kind
only from an object's own name inside a native ComfyUI store; classifying a
kind for a diffusion checkpoint discovered in another store belongs with the
`image@1` service work, not yet built. Nothing here bundles an engine or a
model. `Lend` carries no `family` field this release; MODEL-C2 already
closes the descriptor keys the ledger could record, and adding it is
scheduled with `link_type` and `forbidden` above (providers review F13).
