# History: abstraction.storage/lend@1

Linked from [CONTRACT.md](CONTRACT.md)'s "Reading this page."

## Superseded ids

- **`LEND-1`..`LEND-18`** became `MODELBRIDGE-L1`..`MODELBRIDGE-L18`, the
  same number, per `research/vocabulary/RENAME-PLAN.md`'s modelbridge step 1
  and `research/vocabulary/DECISION.md` S3/S4. `LEND` is retired and never
  reused.

| retired | now | rule |
| --- | --- | --- |
| `LEND-1` | MODELBRIDGE-L1 | object naming |
| `LEND-2` | MODELBRIDGE-L2 | engine naming |
| `LEND-3` | MODELBRIDGE-L3 | a lend is a link |
| `LEND-4` | MODELBRIDGE-L4 | copy on request |
| `LEND-5` | MODELBRIDGE-L5 | refusal leaves nothing behind |
| `LEND-6` | MODELBRIDGE-L6 | naming the link |
| `LEND-7` | MODELBRIDGE-L7 | one directory per lend |
| `LEND-8` | MODELBRIDGE-L8 | the ledger is authoritative |
| `LEND-9` | MODELBRIDGE-L9 | write before reply |
| `LEND-10` | MODELBRIDGE-L10 | one lend per engine |
| `LEND-11` | MODELBRIDGE-L11 | what `Unlend` removes |
| `LEND-12` | MODELBRIDGE-L12 | lending is gated by a rule |
| `LEND-13` | MODELBRIDGE-L13 | `Unlend`'s right |
| `LEND-14` | MODELBRIDGE-L14 | the peer check |
| `LEND-15` | MODELBRIDGE-L15 | sizes |
| `LEND-16` | MODELBRIDGE-L16 | no bytes read |
| `LEND-17` | MODELBRIDGE-L17 | the ledger is an operator record |
| `LEND-18` | MODELBRIDGE-L18 | ComfyUI's folders |

### The memory binding gains its fields, 2026-10-01

The inference provider kit fills a model into a read-only memory section
(VISION 2026-10-01, `research/model-locators/MEMORY-LENDING.md`, inference
INF-MS1..INF-MS6), and the binding kinds this service declared gained what
that fill reads and produces.

| id | before | now |
| --- | --- | --- |
| MODELBRIDGE-L19 | `compressed` carried the container, the member chain and the method; nothing produced `memory` | `compressed` also carries its stream's offset in the container file, its stored size and CRC-32; `memory` is produced by the provider kit's fill, with the section handle, length, digest, chunk digests and the binding it was filled from; no source answers it |

## Measured

- **`TestLiveLendTheSmallestGGUFToLMStudio`** (`OA_LIVE_LEND=1`), 2026-09-22:
  lent a 45,949,216-byte file from the `jan` store by hard link in 6.3
  seconds end to end; the first lend refused `not_permitted`, the lend after
  the rule placed one hard link, LM Studio listed it as
  `text-embedding-sentence-transformer-mini` inside the 30-second window,
  and `unlend` removed the link and its directories. The engine kept naming
  the model in its own list immediately afterwards (MODELBRIDGE-L8). See
  [README.md](README.md#measured) for the walkthrough.
- **LM Studio indexing**, 2026-09-22: indexed
  `sentence-transformer-mini.gguf` within eight seconds and skipped an
  identical `model.gguf` in the same directory entirely (MODELBRIDGE-L6).
- **LM Studio's `jitModelTTL`**, 2026-09-22: read as up to 3600 seconds on
  the measured machine (MODELBRIDGE-L8, providers review F9); the value is
  the person's own LM Studio setting, not a bound this service states.

## Design records

- `research/model-bridge/DECISION.md` §4 — the decision that lending is a
  different provider from the storage inventory's read-only adapters.
- `research/model-bridge/EXPERIMENT-2026-09-22.md` — what was measured on a
  real machine, cited above.
- `research/vocabulary/DECISION.md` D4, D11, D12, D78, D88 — service,
  server, engine, `link_type`/folder, and the lend right's resource string.
  Applied in this release: D4 (service), D11/D88 (the `server:<name>` prose
  word, S13 table entry pending rights' own dual reader), D12 (engine
  unchanged), the `folder` wording of D78. Not applied this release:
  `Lend.kind` → `link_type` on the wire, scheduled for
  `research/vocabulary/RENAME-PLAN.md` §4's third release.
- `research/vocabulary/DECISION.md` §3 (S1-S13) — the contract shape this
  page and its "Reading this page" section follow.

## Reviewed and applied

- `research/reviews/providers-2026-09-23.md` F3 — who reads the ledger was
  stated twice, differently. The omission sentence is dropped from
  MODELBRIDGE-L12 and from `LendPage`'s doc in `lend.thrift`; MODELBRIDGE-L17
  stays the ledger's own operator-right rule.
- `research/reviews/providers-2026-09-23.md` F7 — the hard-link trigger
  named an engine refusal nobody measured. MODELBRIDGE-L3 drops the engine
  clause; the README's hard-link sentence is corrected.
- `research/reviews/providers-2026-09-23.md` F8 — ComfyUI was in the
  contract and not the README. Added to the README's engines and layout
  sections; MODELBRIDGE-L18 now says its folder list is a subset of
  ComfyUI's own.
- `research/reviews/providers-2026-09-23.md` F9 — LEND-8's ceiling was
  stated as a fact this service enforces. Reworded to name it as the
  measured machine's own LM Studio setting.
- Review of `42e34a90f` (2026-10-01), blocking — `weights()` fell back to a
  manifest's first entry with any locator, so a Hugging Face snapshot
  holding only `checkpoint.pkl` and `config.json` resolved to a file, and a
  STORED nested `torch.zip` (a torch.save checkpoint) resolved to a region.
  MODELBRIDGE-L21 binds only a model format and limits the fallback to the
  `projector` and `vae` roles; `.pkl` and `.pickle` join MODELBRIDGE-L20's
  pickle names as a second guard. Regressions `TestASnapshotManifestWithNoWeightsIsNotBound`
  and `TestANestedTorchZipIsNotBound` fail with the previous `object.go`
  overlaid.
- Review of `05fb3042e` (2026-10-01), hardening — the four-byte header read
  opened any path, and a FIFO where an Ollama blob should be would block
  `open(2)` on Unix while `Lend` held the bridge's lock. The read now opens
  non-blocking on Unix and requires the opened handle to be a regular file
  (MODELBRIDGE-L21). `TestAFIFOObjectIsNotBoundAndDoesNotBlock` blocked with
  the previous `source/manifest.go` overlaid, under Linux.
- Review of `f15572bf2` (2026-10-01), decided by the coordinator —
  whisper.cpp's ggml `.bin` (magic `lmgg`) holds tensors and a vocabulary
  and no code. MODELBRIDGE-L20 no longer counts it a pickle and
  MODELBRIDGE-L21 binds it as the `ggml` format, through storage
  `source.IsPickle` and `source.ModelFormatOf` (LS-1, LS-2), the one owner
  of both rules. `TestLendRefusesPickles` resolves a ggml `.bin` beside the
  refused pickles.
- Same review as `42e34a90f` — 0.3.0 published this module as source with no pickle rule
  and with `.pth` and `.ckpt` files as manifests, so a self-built
  `modelbridged` could have lent a pickle. No 0.3.0 package installed or
  registered `modelbridged`; reconciling such ledger entries is a TODO row.

## Reviewed and deferred

- `research/reviews/providers-2026-09-23.md` F1 — the peer check's account
  mismatch and a verified caller's missing rule both read `not_permitted`.
  `abstraction-provider-modelhost`'s own peer check already reads
  `forbidden` for the same distinction (MODELHOST-R3). Closing this needs
  `forbidden` added to `LendOutcome`, `UnlendOutcome` and `LendsOutcome` in
  `lend.thrift`, a wire change `research/vocabulary/RENAME-PLAN.md` §4
  schedules for the third release; recorded as a Divergence in CONTRACT.md
  until then.
- `research/reviews/providers-2026-09-23.md` F6 — `Lend.kind` carries the
  `Link` enum where the concept is really the ComfyUI folder-classification
  word D78 decided (`folder`). The prose fix (MODELBRIDGE-L18 says folder,
  `bridge.KindOf`'s doc already did) is applied; the field rename to
  `link_type` is the same third-release wire change as F1's `forbidden`, and
  carries an S13 table entry meanwhile.
- `research/reviews/providers-2026-09-23.md` F13 — "family" in MODELBRIDGE-L6
  and MODELBRIDGE-L7 was not tied to `MODEL-C2`. MODELBRIDGE-L6 now cites
  `MODEL-C2`. `Lend` gaining a `family` field needs a `lend.thrift` schema
  change and a codegen run this worker did not make; recorded here as not
  built until a release schedules it.
