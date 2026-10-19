# abstraction-provider-modelbridge

*For operators and provider authors. Applications reach it through the OA
runtime.*

This provider lets an engine on this machine use a model file that another
app here already holds, by linking to the file where it lies.

**What's new here:** A model file one application downloaded is served by
another application's engine through a link, recorded in a ledger and reversed
by `unlend`, with no second copy.

abstraction-provider-modelbridge is the model bridge: it **lends** a model
file one store on this machine already holds to an engine installed here,
so that engine serves it, without that engine's own management layer ever
knowing the file came from somewhere else. A lend is a link — a symbolic
one, a hard one where the operating system refuses the process a symbolic
link and both sit on one volume, and a copy only when the caller asks for
one — never a move, a rename or a deletion of the original. Every lend is
recorded in a **ledger**, this
provider's own record of which links it placed and which are still in
force; the ledger, not the engine's own list, is what says whether a lend
is currently in force. It serves
[`abstraction.storage/lend@1`](https://github.com/openabstractions/abstraction-storage/blob/main/lend.thrift) to the runtime, which decides
each application's right and forwards; applications never reach it directly.

## Start

Install the runtime first: https://openabstractions.org/adopt.html

```
modelbridged serve --endpoint NAME --state DIR [--runtime-program PATH]
                   [--config FILE] [--home DIR]
                   [--lemonade-base URL]
modelbridged lends --state DIR
modelbridged help
```

`serve` answers `Lend`, `Unlend` and `Lends` on the named local endpoint
until interrupted. It accepts only the installed runtime executable beside
the provider, including the windowless executable on Windows. Pass
`--runtime-program` for a runtime in another directory. `--state` is the provider's own directory: the ledger
(`lends.json`) and the directory of links lent to Lemonade live in it.
`lends` prints the ledger once, as JSON on stdout: a provider diagnostic,
never an application interface. Running `modelbridged help` (or with no
arguments) prints the option list in full. From the command line, once the
runtime is installed:

```
openabstractions models lend <store>/<id> --to lmstudio
openabstractions models lends
openabstractions models unlend <entry>
```

## Recover an unfinished or legacy lend

An interrupted placement can return `unavailable: an unfinished staged file
needs reconciliation`. A later start can instead report `modelbridge:
unfinished staged lend needs reconciliation: <stage>` or `modelbridge:
unfinished lend needs reconciliation: <link>`, or `modelbridge: unfinished
created directory needs reconciliation: <directory>`. `Unlend` of an older entry can
return `unavailable: this legacy entry has no durable ownership record`.
These refusals preserve a file whose ownership the provider cannot prove.
Current receipts include a creation-time discriminator. Linux filesystems must
provide birth time through `statx`; otherwise the provider reports
`modelbridge: filesystem birth time unavailable for ownership receipt` and
refuses the operation. Older receipts with only `volume` and `index` cannot
prove ownership after a file index is reused. Preserve the named file and
ledger for reconciliation rather than filling in missing receipt fields.

1. Find the provider's `--state DIR` (or `MODELBRIDGE_STATE`); its record is
   `DIR/lends.json`. Find the `modelbridged` PID whose command line names that
   state directory. Stop **that exact provider process**, and prevent its
   supervisor from restarting it while the record is edited. Pause new
   lending calls. Leave the engine, other providers and
   the shared runtime alone. Copy `lends.json` to a dated backup outside the
   state directory. Keep the original model file and every engine path in
   place during inspection.
2. Open the backed-up JSON and the current `lends.json` in a JSON-aware
   editor. Check `version` is `1`. A committed row is in `lends`; its `ID`,
   `Engine`, `Store`, `Object`, `Link` and `Kind` name the lend. A matching
   `owned[ID]` receipt contains `file` and `parent`, each with native `volume`
   and `index` numbers. Current receipts also contain `version`,
   `birth_seconds` and `birth_nanos`. `dirs` records directories this provider
   created.
   An unfinished row is in `pending`; `entry.ID`, `entry.Link`, and `stage`
   name its exact paths. An optional `creating` path marks a directory whose
   creation was recorded before `mkdir` but whose native ownership was not
   durably recorded afterward. Inspect those paths without following a symbolic
   link. Check that `entry.Link` lies under the engine's configured models
   directory described in [The engines](#the-engines). For LM Studio, check
   its selected downloads folder; for Lemonade, check `extra_models_dir`;
   for ComfyUI, check the model-kind folder. The ledger does not contain the
   original source path, so `Store` and `Object` alone do not prove a file's
   ownership. If a path or engine setting disagrees, keep the backup and
   leave the record unchanged.
3. For a `pending` row whose `file` is `{"volume":0,"index":0}`, inspect
   its `stage`, `entry.Link`, and optional `creating` directory. If all named
   paths are absent and no undo quarantine for `creating` remains, restart the
   provider; startup removes the empty intent when any recorded `parent`
   identity still matches. A `directory changed` or `created directory
   quarantine needs reconciliation` error leaves it for inspection. Never
   remove a quarantine by matching its name alone. If any named path exists,
   preserve it. To restore provider startup, remove **only that pending object**
   from a copy of `lends.json`, validate the JSON and compare it with the
   backup, then replace `lends.json` while the provider is still stopped.
   Keep `version`, `lends`, `owned`, `dirs` and every other pending object
   unchanged. Restart the provider. Any remaining final file is unrecorded;
   another lend to that name correctly returns `link_refused`. A staged file
   or `creating` directory left in place remains unowned by the ledger and
   available for later inspection. If the operator decides to withdraw an
   exact artifact, first move it to a new, empty recovery location on
   the same filesystem outside the engine's model tree, with no overwrite,
   and retain it there. Preserve a foreign replacement in place or at that
   recovery location. Never infer directory ownership from `creating` alone.
4. For a legacy `lends` row with no `owned[ID]`, or an older two-field
   `owned[ID]` receipt, automatic `Unlend` cannot establish which object stands
   at `Link`. To withdraw its ledger claim,
   preserve the exact `Link` object in a new, empty recovery location as
   above, or leave it in place as an unrecorded engine file. Back up the
   ledger, remove **only that one `lends` row and its matching `owned[ID]` if
   present** in a JSON-aware editor, validate and compare it, then replace the
   record while the provider is stopped.
   Restart the provider. A link left in place blocks a new lend at that name
   with `link_refused`; an archived link leaves the name free for a fresh lend
   with a new ownership receipt. This manual withdrawal does not promise the
   engine immediately forgets a cached model.

`modelbridged lends --state DIR` runs startup recovery before it prints the
committed entries, so it cannot inspect a ledger blocked by an ambiguous
pending row. Read the backed-up `lends.json` directly in that case. A nonzero
`pending.file` has an ownership receipt and startup normally finishes or
removes it; errors such as `staged lend was replaced`, `unfinished lend path
was replaced`, `lend directory changed`, or `foreign replacement retained at
<path>` need case-specific inspection. Keep those paths and receipts intact.
Never create an `owned` receipt by guesswork, delete the lend root, or remove
all pending rows to make startup pass.

## What it needs

**Go** 1.26 or later ([`go/go.mod`](go/go.mod)), plus
[`abstraction-identity`](https://github.com/openabstractions/abstraction-identity),
[`abstraction-storage/go`](https://github.com/openabstractions/abstraction-storage)
and `abstraction-storage-over-local-stores/go`, whose adapters this layer
resolves names through.

## How the runtime uses it

The runtime launches the declared program, probes it with
`abstraction.facade/endpoint@1` `Describe` over a connection that requires
that program as the server, and publishes `abstraction.storage/lend@1` on
its own endpoint for applications. From the command line:

```
openabstractions provider add modelbridge --program /abs/path/modelbridged \
  --provider-endpoint modelbridge-v1 --contract abstraction.storage/lend@1 \
  --arg serve --arg --endpoint --arg {endpoint} --arg --state --arg /abs/state
```

The declaration writes no rule. Lending is permitted once, by the person, at
first use: `abstraction.storage/lend` on resource `server:<name>` (the wire
still carries `engine:<name>` until rights' own dual reader ships,
`CONTRACT.md`'s "Reading this page"). A
program's first lend is refused `not_permitted` and puts the question where
the person answers it. Applications never reach this service directly:
their calls travel through the runtime, which binds the caller from the
connection's program proof, decides the rule, and forwards.

## Measured

`serve`'s `TestLiveLendTheSmallestGGUFToLMStudio` (`OA_LIVE_LEND=1`) builds
this provider and the real `inventoryd`, declares both to an isolated
runtime, reads the smallest GGUF the composed inventory lists outside LM
Studio's own store, and lends it to the LM Studio running on the machine.
On 2026-09-22 it lent a 45,949,216-byte file from the `jan` store by hard
link in 6.3 seconds end to end: the first lend refused `not_permitted`, the
lend after the rule placed one hard link, LM Studio listed it as
`text-embedding-sentence-transformer-mini` inside the 30-second window, and
`unlend` removed the link and its directories. The engine kept naming the
model in its own list immediately afterwards, which is CONTRACT.md
MODELBRIDGE-L8. Recorded in [HISTORY.md](HISTORY.md).

## Why it is its own provider

`abstraction-storage-over-local-stores` reads the stores. Its `README.md`
and its `inventoryd help` both promise the same thing in the same words: it
"writes, moves or deletes nothing in any store it reads." Lending writes
into another application's directory, which is the opposite promise. It is a
different provider with a different trust and lifecycle. The bridge reads
the stores through that layer's own adapters
rather than scanning the machine a second time: `bridge.LocalStores`
composes `snapshot.Take`, and everything it learns about a store comes from
there.

## The engines

**LM Studio** lends into its own models folder, found the way that store's
adapter finds it (`~/.lmstudio-home-pointer`, then `~/.cache/lm-studio`,
then `~/.lmstudio`, with `settings.json`'s `downloadsFolder` deciding the
models root). Everything a lend creates sits under
`<models root>/openabstractions-lend/<family>/<file>`. LM Studio reads new
subdirectories without being told and without a restart.

**Lemonade** lends into one directory this provider owns, under `--state`,
and tells Lemonade to scan it. Lemonade's own documentation names
`extra_models_dir` as a setting changeable at runtime through `POST
/internal/set`, so no person edits `config.json` for a lend. Without
`--lemonade-base`, and whenever that server does not answer or refuses the
setting, a lend to `lemonade` is refused `unsupported_engine`: this
provider never edits another program's configuration file.

**ComfyUI** lends into one folder per kind under its `models` directory
(CONTRACT.md MODELBRIDGE-L18), a subset of the folders ComfyUI's own
`folder_paths.py` keeps (`checkpoints`, `loras`, `vae`, `clip`, `unet`,
`diffusion_models`, `controlnet`, `embeddings`, `upscale_models`,
`clip_vision`, `text_encoders`). ComfyUI re-walks a folder it already knows
when that folder's own modification time changes, so it reads a lend
without a restart the same way LM Studio does; nothing here writes
`extra_model_paths.yaml`, which is ComfyUI's own file, read once at its own
startup.

## Layout

- **`go/bridge/`** — the operation. `object.go` resolves the inventory's
  naming (a store and an id) to a binding on this machine, a file or a
  region or compressed member of a zip as `binding.go` defines them
  (MODELBRIDGE-L19), and derives the family name a lend uses; `link.go` places one link and prunes what a lend
  created; `ledger.go` is the record, written in one rename; `engine.go` is
  LM Studio, Lemonade and ComfyUI; `bridge.go` is `Lend`, `Unlend` and
  `Lends` and their typed refusals.
- **`go/service/`** — `host.go` serves the service on the shared
  identity-bound native transport (a named pipe on Windows, an
  account-scoped socket elsewhere), admitting only the designated runtime program.
- **`go/cmd/modelbridged/`** — the one binary this layer produces.

## Limits

- **The Lemonade path is untested against a real Lemonade.** It is
  exercised against a fixture server in this module's own tests; a real
  Lemonade with a model store was not installed on the machine this was
  measured on.
- **`Lend` names no engine process: it neither starts nor stops one.**
  Spawning an engine for a file no installed engine can serve is
  DECISION.md rule 4's third step and belongs to a later slice.
- **A diffusion checkpoint's kind is classified only inside a native
  ComfyUI store.** Classifying one discovered in another store belongs
  with the `image@1` service work, not yet built.
- **`Lend` carries no `family` field this release.** MODEL-C2 already
  closes the descriptor keys the ledger could record; the field is not yet
  built (CONTRACT.md "Not built").
- Nothing here bundles an engine or a model.

## Status

Development provider for OA 0.3.0. The
[Go source release](https://github.com/openabstractions/abstraction-provider-modelbridge/releases/tag/go/v0.1.0)
is tagged `go/v0.1.0`. This repository has no standalone release assets. The
[installation guide](https://openabstractions.org/adopt.html) describes the
available release packages; the source instructions above describe this provider.

## Licence

Apache-2.0. See [LICENSE](LICENSE), published beside this file at the
repository root.

## Design records

- `research/model-bridge/DECISION.md` section 4 — the decision that lending
  is a different provider from the storage inventory's read-only adapters.
- [HISTORY.md](HISTORY.md) — measurements, superseded ids and the reviews
  applied to this release.
