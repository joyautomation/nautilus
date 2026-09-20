# Design brief: Allen-Bradley Logix as a nautilus target

Status: **analysis only.** Nothing built. Written 2026-09-20 on the
`logix-target` branch (worktree `~/Development/joyautomation/nautilus-logix`)
after a survey of the nautilus tooling contract, the `eip/` stack, the language
pipeline, and the live state of `rockwell-vm`.

**The question asked:** can the nautilus extension and CLI drive an
Allen-Bradley Logix controller through the Studio 5000 Logix Designer SDK —
create programs, download, upload, monitor, set values — *"just as if it were
the nautilus runtime"*?

**The short answer:** the monitoring and project-lifecycle halves are largely
already built or already proven here, and can ship. Literal runtime parity
cannot be reached, and the two features that break it are the two features
nautilus is most differentiated by. That is not a reason to drop the project —
it is a reason to aim it somewhere better. §8 makes the recommendation.

---

## 1. What "the nautilus runtime" actually is, as a contract

There is no `Runtime` interface in the repo. `server.New` takes a concrete
`*runtime.Runtime` (`server/server.go:442`) and the handlers call
`rt.Tags()`, `rt.Program()`, `p.SwapWarm()` directly. So a Logix backend
**cannot be a Go plugin** — there is nothing to implement.

The real seam is the HTTP API. Anything that serves these JSON shapes is
indistinguishable from a controller to the tooling, and the surface is
remarkably small:

| Consumer | Endpoints it actually calls |
|---|---|
| VS Code extension | `GET /api/stream`, `GET /api/state`, `POST /api/tags`, `GET /api/program`, `PUT /api/program`, `POST /api/program/rollback` |
| `nautilus pull` | `GET /api/program` only |
| `nautilus run/build/check/test/lsp` | **nothing** — none of them opens a socket to a runtime |

Six endpoints. No auth by default, no discovery protocol (`nautilus.runtimeUrl`
is a VS Code setting, default `http://localhost:8080`), no version negotiation
beyond the capability booleans on `GET /api/meta`.

Two consequences worth stating plainly:

- **`nautilus check`, `nautilus test` and the LSP are already target-agnostic.**
  They parse the workspace and never talk to a controller. Whatever the runtime
  is, editing, diagnostics, go-to-definition and acceptance tests keep working
  unchanged. That is a large part of the product that needs *no* work.
- **`GET /api/program` returns `source` as IEC text, and the tooling diffs it
  character-for-character** against workspace files after stripping the library
  prelude (`onlineEdit.ts:330-349`, `cmd/nautilus/pull.go:165`). Anything that
  cannot round-trip byte-identical composed source breaks pull, diff, and the
  sync status bar. This is the hinge the whole "is it really the same?"
  question turns on — see §6.1.

The graceful-degradation lever already exists: `programInfo.Editable = false`
cleanly disables download and rollback in the extension UI
(`onlineEdit.ts:257-262`) while leaving live values, diff, pull and status
working.

---

## 2. What the Studio 5000 SDK gives us

Verified against `rockwell-vm` (SDK **2.02.00**, Python wheel
`logix_designer_sdk-2.0.2`, Studio 5000 v21–v38 side by side, FactoryTalk Linx
6.60, FactoryTalk Logix Echo 4.00) and the *Logix Designer SDK Getting Results
Guide* LDSDK-GR001D-EN-P, March 2026.

The shipped Python examples are the authoritative capability list:

| Capability | Example | Notes |
|---|---|---|
| Open / save / save-as | `open_and_save_file.py` | ACD ⇄ L5X ⇄ L5K. This is the n26 conversion path. |
| Create a new project | `create_new_project.py` | Built on the VM at `C:\acdwork\CreateNewProject`. |
| Partial import from L5X | `partial_import_offline.py` | XPath-addressed, down to a **rung range**; collision options. **Offline only.** |
| Partial export to L5X | `partial_export_offline.py` | Same addressing, other direction. |
| Build / verify | `build_project.py` | The compile gate. |
| Download | `download_project.py`, `single_acd_many_controller_download.py` | Full controller download. |
| Upload | `upload_project.py`, `upload_to_new_project.py` | Controller → ACD. |
| Go online / offline, read mode, change mode | `go_online_offline.py`, `read_controller_mode.py`, `change_controller_mode.py` | |
| Get / set tag value | `get_tag_value.py`, `set_tag_value.py` | Typed per data type (`set_tag_value_dint`), XPath tag path, `OperationMode.OFFLINE` \| `ONLINE`, **one tag per call**. |
| Comm path | `get_comm_path.py`, `set_comm_path.py` | FactoryTalk Linx syntax: `AB_ETH-1\10.88.45.25\Backplane\0`. |
| Enumerate executables | `get_all_executables.py` | Programs / routines / AOIs. |
| SD card, safety signature, source protection, convert version/controller type | several | Not on the critical path. |

**The constraints that shape the architecture** (guide §Considerations, plus
what the VM shows):

- **The SDK server listens on `127.0.0.1:53204` only.** Confirmed live:
  `LdSdkService` binds loopback and `::1`, nothing else. There is no remote
  protocol. *Every* remote design therefore needs an agent process on the
  Windows box (or an SSH tunnel, which is the same thing with worse ergonomics).
- FactoryTalk **Linx** is the only supported comms; RSLinx is not. The
  controller must already be visible in the Linx Network Browser.
- Windows, 64-bit, licensed Studio 5000. The SDK wheel is **not
  redistributable**.
- Max project 500 MB; **max 30 kB for a single SDK operation data file** — this
  appears to cap partial-import L5X fragments and needs measuring (spike S4).
- Projects v31+ only.
- Visual Studio is required on the *development* machine for the C# client.

And the one that matters most for the product shape: **there is no online-edit
API.** No test/assemble/accept edits, no routine-level online change. Partial
import is explicitly offline. The only way code reaches a controller is a full
download, which puts it in Program mode.

---

## 3. What we already own

This is the part that makes the project plausible rather than speculative.

**In the nautilus repo:**

- `eip/cip/` (~2.9k LOC) — a pure-Go EtherNet/IP + CIP stack. Encapsulation,
  CPF, message router, EPATH with both symbolic (0x91) and logical/instance
  addressing, Forward_Open (standard + large), multi-service packets, Class 1
  implicit I/O.
- `eip/logix/` (~1.7k LOC) — the Logix tag surface: Class 3 connected
  messaging, `ReadTag`/`ReadTagFragmented`/`WriteTag`, batched `ReadTags`,
  symbol browse over class 0x6B, **UDT template upload** over class 0x6C,
  nested template walking, Logix STRING shape handling.
- `eip/driver.go` — a production `io.Driver`: scan classes with per-class
  rates, write-on-change, per-tag quality, startup validation against the live
  controller, and **leaf mode** — when a struct root read is refused (CIP 0x0F,
  typical of AOI backing tags) the binding permanently drops to batched
  member-by-member reads.
- `eip/logixserver/` (~1.3k LOC) — an in-repo ControlLogix **emulator** that
  answers as a 1756-L83E, including the Program Name object pycomm3 wants.
- `eip/codegen/` + `nautilus eip import|browse|tags` — live browse → UDT shapes
  as IEC `TYPE` blocks, a driver manifest, and a nautilus tag file.
- `examples/client60/` — a committed, working manifest pointing at a **real
  Logix PLC**, with imported UDTs (`Analog_Input`, an AOI type), 14 bindings,
  leaf mode exercised, ladder logic running derived alarms over the PLC's
  process values, and Sparkplug publishing on top.
- `lang/stgen` — builds IEC type declarations in Go and **validates them by
  compiling the output back through the parser**. This is the exact shape any
  text emitter should take, and the precedent already works in production
  (`eip/codegen`).
- `tools/vscode-iec` — ladder and FBD diagram editors, live-value decorations,
  and (on the `diff-revisions` branch) diagram diffs between any two git
  revisions.

**Outside the repo:**

- `~/Development/pomona/aep/l5xgen` — ~2.4k lines of pure-stdlib Python that
  **generates and lints Studio 5000 L5X import files**, with every lesson from
  real import/verify failures baked in as a build-time check (pre-v36
  mnemonics, quoted CPT expressions, empty rungs, over-long descriptions,
  truncated tag names, continuous tasks in a redundant controller, local-chassis
  modules, L8x-unsupported attributes). Plus a PLC-5 `.PC5` parser and a
  PLC-5 → Logix rung translator. Grown out of a delivered conversion.
- `rockwell-vm` (incus, 10.154.92.130, SSH alias `rockwell`) — Studio 5000
  v21–v38, SDK 2.02, FactoryTalk Linx, **FactoryTalk Logix Echo 4.00 with
  ControlLogix 5580 v33–38, 5590 v38, CompactLogix/GuardLogix 5380 v36–38
  emulators installed**, and prior builds left in place at `C:\acdwork`:
  `OpenAndSaveFile`, `CreateNewProject`, `PartialImportOffline`.
- Proven at field scale: 52 ACD → L5X conversions driven headlessly from Linux,
  ~20 s each (the n26 work, 2026-08-22/28).

Logix Echo is the quiet headline. An emulated 5580 speaks EtherNet/IP Class 1
and 3 — which means **`eip/` can talk to it, the SDK can download to it, and
the whole loop closes with no hardware.** CI becomes possible.

---

## 4. Proposed architecture: three planes, one facade

The single most important design decision is **not to route live data through
the SDK.** `set_tag_value` is one tag per call over a loopback RPC with a
500 MB project open; it is a commissioning tool, not a scan-rate transport. We
already have a scan-rate transport.

```
                 ┌──────────────────────────────────────────┐
  VS Code ──────▶│  nautilus logix serve   (Go, Linux)      │
  nautilus CLI   │  serves the six endpoints of §1          │
                 └───────┬──────────────────────┬───────────┘
                         │                      │
        online plane ────┘                      └──── project plane
   EtherNet/IP, pure Go, no Rockwell software      HTTP → logixd (Windows)
   eip/logix + eip/driver                          → Logix Designer SDK
   /api/stream, /api/state, POST /api/tags            :53204 loopback
   tag + UDT browse                                 open · partial import ·
   milliseconds                                     build · download · upload ·
                                                    mode · comm path
                                                    seconds to minutes
                         ▲
                         │  codegen plane (offline, pure, no Rockwell software)
                         │  nautilus sources ──▶ L5X   /   L5X ──▶ nautilus models
                         └───────────────────────────────────────────────────────
```

### 4.1 Online plane — done

Adapt `eip/driver.go`'s polled tag map into a `server.Frame` and serve
`/api/stream` + `/api/state`; route `POST /api/tags` to `logix.WriteTag`. The
extension's live-value pills, hover, right-click set-value and FB-instance
monitoring all start working against a real PLC. `Frame.locals` has a
reasonable analogue in Logix program-scoped tags (`Program:Main.Foo`), which
`eip/logix/browse.go` already canonicalizes.

Estimated effort: **days**, not weeks. This is adapter code over shipped,
field-proven packages.

### 4.2 Project plane — `logixd`, a Windows agent

Because the SDK is loopback-only, a small service has to live beside it.

- **Language:** Python for v1. The SDK's Python client is first-class from 2.01
  and the examples are Python; iteration speed matters more than packaging
  while the API shape is unsettled. C# is the fallback if single-exe packaging
  or a configurable SDK port becomes necessary (only the C# client can move
  off 53204).
- **Shape: a job queue, not request/response.** `LdSdkService` is a singleton,
  one project opens at a time, and an open costs ~20 s. `POST /jobs` → job id →
  poll or SSE for progress and logs. Long operations (download, upload,
  convert) must not sit on an HTTP request.
- **Safety gating is a first-class feature, not a footnote.** A download stops
  a controller. `logixd` carries an **explicit comm-path allowlist** in its own
  config — not supplied by the caller — and refuses anything else. Separate
  tokens for read-only operations and for download. Structured audit log of
  every job with caller, comm path, project hash, outcome.
- **Transport:** Tailscale + a bearer token, or mTLS. Never expose it to a
  plant network unauthenticated.
- Two gotchas already learned on this VM and worth carrying forward: Windows
  OpenSSH kills a "detached" child when the session closes, so long work runs
  as a Scheduled Task or a real service, not `Start-Process`; and the
  "`LdSdkService` wedges after one operation" claim **did not reproduce** on
  2026-08-28 (eight back-to-back conversions on one service instance) — don't
  build the restart-every-file workaround back in without re-measuring.

### 4.3 Codegen plane — two directions, wildly different difficulty

**L5X → nautilus models (read).** A pure-Go parser in `lang/l5x`. RLL routines
→ the existing `lang/ld` graph model; ST routines → the `lang/st` AST; UDTs →
`stgen` types; tags → a nautilus tag file. No Rockwell software at runtime, no
semantic-equivalence risk (you are rendering, not executing), and it is already
on the README roadmap as "vendor-format import".

This lights up, on Rockwell code, features that already exist and are already
good: the ladder viewer, the FBD viewer, **diagram diffs between git
revisions** (the branch this analysis was written from), hover, and structural
linting. It also fixes a documented gap — `eip/codegen/tags.go:56` notes that
tag descriptions live in the offline project file and a live browse cannot
recover them. An L5X reader recovers them.

**Per unit of effort this is the highest-value item in the entire plan.**

**nautilus → L5X (write).** Much harder, and §6 is mostly about why.

### 4.4 The facade — `nautilus logix serve`

A Go process that serves §1's six endpoints. Live data from the online plane.
`GET /api/program` returns the last pulled-and-normalized L5X (or the ST
transpiled from it, once the reader exists) with `editable: false` until the
download path is trusted. `PUT /api/program` either 403s with a message the
extension surfaces verbatim, or — later — enqueues a gated `logixd` download
job.

The extension needs **no changes at all** for this. That is the payoff of §1's
finding.

---

## 5. What maps cleanly

| Nautilus capability | Logix mechanism | Difficulty |
|---|---|---|
| Live tag values in the editor | `eip/` CIP polling | **Done** — adapter only |
| Set a value from the editor | `logix.WriteTag` | **Done** — adapter only |
| Tag / UDT discovery | `nautilus eip import` | **Done** |
| Tag *descriptions* | L5X reader | Easy, new |
| Upload from controller → text | SDK `upload_to_new_project` → `SaveAs(.L5X)` | Easy — proven, 52 files |
| ACD → L5X at plant scale | SDK, headless | **Done** — productize the n26 work |
| Drift detection (controller vs git) | upload → normalize → diff | Easy–medium; the normalizer is the work |
| Download a reviewed project | SDK `download` + comm path | Medium; the risk is procedural, not technical |
| CI build / verify | SDK `build` + Logix Echo | Medium |
| Alarms, historian, Sparkplug over Logix data | existing nautilus subsystems over the EIP driver | **Done** — `examples/client60` does it today |
| Acceptance tests of *nautilus-authored* logic | unchanged — runs on the nautilus VM | **Done** |

---

## 6. What does not map — the honest part

### 6.1 Byte-exact source round-trip

`GET /api/program` must return text the workspace diffs against, character for
character. A Logix controller does not store your nautilus ST. Three options,
none free:

1. **Workspace is the source of truth; drift is measured at the L5X level.**
   Generate L5X from the workspace, upload the controller's L5X, normalize
   both, diff. You get a real and valuable drift story ("someone changed the
   controller") — but the diff the engineer reads is L5X, not their source.
2. **Stamp provenance into the controller.** A controller-scope tag
   (`NAUTILUS_BUILD`) carrying the source hash, written at download time.
   `dirty` becomes "the controller's L5X no longer hashes to what this build
   produced." Cheap, and it makes the N-25 field-edit story work on Logix.
3. **Full round-trip** (nautilus ST → L5X → upload → parse back → nautilus ST)
   requires the emitter and the parser to be exact inverses. Do not promise
   this.

Recommend 1 + 2. Do not attempt 3.

### 6.2 Warm, per-program download — not possible

`PUT /api/program` warm-swaps one task while the others keep scanning, carrying
retained state by name and type through `ir.MigrateFrame`, with a one-step
rollback and a failed compile leaving the running program untouched.

The SDK has **no online-edit API**. Partial import is offline. The only path to
the controller is a full download that stops it and resets tags to project
values.

This is nautilus's most distinctive runtime feature — the one already slated as
its own piece of content precisely because it is unique. It has no Logix
analogue through this SDK, and pretending otherwise in the UI would be worse
than disabling it. `editable: false` is the honest answer; a deliberate,
confirmed, mode-checked cold download is the eventual answer.

### 6.3 Retained-state migration — not possible

Same root cause. Logix download resets tags to project values; retaining values
across a download is a manual Studio 5000 feature with its own semantics, not
something to emulate.

### 6.4 Semantic equivalence for generated code

If nautilus emits Logix code, "the same program" has to mean the same behaviour
on two different runtimes. The gaps found in the language survey:

- **Integer width and signedness are lost in the IR** — everything collapses to
  `TypeInt` (int64) and `TypeReal` (float64). Emitting L5X needs a Logix
  `DataType` per tag. *An emitter must therefore work from the AST, where
  `VarDecl.Datatype` preserves what was written, not from the IR.*
- **No source positions and no comments survive to the IR**; the ST lexer
  discards comments entirely. Rung comments and tag descriptions cannot ride
  through the ST pipeline — an emitter must work from the per-language graph
  models (`lang/ld/graph.go`, `lang/fbd/graph.go`, `lang/sfc/graph.go`), which
  do keep them.
- **`TIME` is a first-class nautilus type**; Logix has none (DINT milliseconds
  in `.PRE`/`.ACC`).
- **IEC timers/counters don't line up**: `TON.Q/.ET` vs Logix `TON.DN/.ACC`;
  Logix has no `TP`, no `CTUD`, no `R_TRIG`/`F_TRIG` instances (ONS is a rung
  modifier). `lang/ld` already synthesizes implicit trigger instances for
  `+Name`/`-Name` coils; those become explicit Logix tags.
- **User `FUNCTION`s** — Logix ST has no user-defined functions with return
  values. Inline them, or make them AOIs with an output parameter.
- **`FUNCTION_BLOCK` → AOI** is mechanically plausible, but `VAR_IN_OUT` maps
  awkwardly (Logix AOI InOut parameters are by-reference and must be UDTs or
  arrays, not scalars), and **`VAR_EXTERNAL` inside an FB body has no Logix
  equivalent at all** — an AOI cannot reference a controller tag, so every such
  binding must be promoted to a parameter.
- **Arrays with non-zero lower bounds** (IEC allows them; Logix does not) and
  **multi-dimensional arrays** (parsed by nautilus, rejected by
  `eip/codegen`).
- **`CONTINUE`** — no Logix equivalent; needs a flag-and-IF rewrite.
- **`STRING`** — nautilus UTF-8 Go strings and their builtins vs Logix's
  fixed-length `DINT LEN + SINT DATA[82]` UDT.
- **The `PID` builtin FB** vs Logix PID/PIDE — entirely different tag
  structures, no mechanical mapping.
- **SFC** is already flattened to ST with generated retained state before it
  reaches the IR; emitting from that would produce unreadable ST, not a Logix
  SFC routine. A real SFC emitter must consume `lang/sfc/graph.go`.
- **The scan model**: "one nautilus task = one Logix Program + MainRoutine" is
  a decision, not a derivation, and tag *roles*
  (`input/output/setpoint/state`) have no L5X representation.

None of these is unsolvable. All of them are decisions that must be made,
documented, tested, and then maintained across Rockwell versions forever. That
is the real cost.

### 6.5 Acceptance testing

`nautilus test` runs on a virtual clock — that is what makes it deterministic
and fast. Logix Echo runs on a real one. Logix conformance testing is therefore
a **separate, slower harness** (download to Echo, drive it over CIP, compare
against the nautilus VM running the same source), not an extension of
`nautilus test`. Worth building if §7 Tier B is pursued; it is the only thing
that would make generated code maintainable.

---

## 7. Feasibility and maintainability

### 7.1 Feasibility

Split the project in two and the answer stops being ambiguous.

**Tier A — Logix as a monitored, version-controlled, CI-gated target.**
No code generation. Nautilus supplies the software-engineering layer; Logix
stays the runtime and the program of record. Feasibility: **high.** Most of the
hard parts are built (`eip/`), proven (52 headless conversions), or cheap
(L5X reader, normalizer, agent). Roughly a quarter of focused work to something
shippable.

**Tier B — Logix as a code-generation target.**
Nautilus sources compile to L5X and download. Feasibility: **possible but
open-ended.** `l5xgen` proves the mechanics and already encodes the lint
lessons; the unbounded part is §6.4 — semantic equivalence — and it does not
converge, it accumulates. Only attempt it behind a conformance harness and a
*documented, enforced subset* of the nautilus language.

### 7.2 Maintainability — the risks, ranked

1. **Licensing and distribution, the structural one.** The SDK is not
   redistributable and requires a licensed Studio 5000 per machine, plus
   FactoryTalk Linx and activation. You cannot ship this in a container and you
   cannot run it as SaaS. It is a customer-installed Windows agent or an
   internal consulting tool. That constraint should shape the business model
   before it shapes the code.
2. **A compatibility surface you don't control.** Studio 5000 v31–v38 today,
   SDK 2.0x, L5X schema per major rev, controller firmware revs. `l5xgen`'s
   lesson catalog — legacy mnemonics, redundancy rules, L8x-unsupported
   attributes — is already a preview of this tax. Rockwell revs annually.
3. **Opaque, unattended failure.** Already measured: automating a GUI-first
   vendor tool is 10% the API call and 90% making it survive being run without
   a human. Budget for that, not for the happy path.
4. **Throughput.** Singleton service, one project at a time, ~20 s per open.
   The API must be job-shaped from day one; retrofitting that is painful.
5. **Safety and blame.** A bug in your download path stops someone's plant.
   Hard gating, allowlists, audit logs, and a strict rule that CI never
   auto-downloads to production.
6. **Support surface.** When Rockwell changes something, the customer calls
   you.

### 7.3 The counterweight

These risks are real, and they are also **unusually well hedged in this
specific case**, because the expensive prerequisites are already paid for: a
working CIP stack, a Logix emulator, an L5X generator with a battle-tested lint
catalog, a headless SDK driver proven at 52 files, diagram editors with
revision diffing, and a lab VM with eight Studio 5000 versions and the
full Logix Echo controller catalog installed. The marginal cost of Tier A is
genuinely low, and Tier A is the part with a market.

---

## 8. Recommendation

**Build Tier A. Spike Tier B behind it. Do not promise runtime parity.**

The framing that works is not "nautilus replaces Studio 5000." It is:

> **You don't have to replace the PLC to fix the workflow.**
> Your Logix code lives in git as text. Every change is diffed and reviewed.
> CI verifies it against an emulated controller. The controller is checked for
> drift against the repo. You watch live values and write setpoints from your
> editor. Nothing about the runtime changes.

That aims the existing product at the largest installed base in the industry,
it requires almost no new semantics, and every piece of it doubles as
infrastructure Tier B would need anyway. It is also the honest version — and
"here is precisely what we can't do on Logix, and why" is a *better* argument
for the nautilus runtime than a leaky imitation of it.

---

## 9. Plan

### Phase 0 — four spikes, ~1 week. These gate everything.

Run them on `rockwell-vm`; no hardware and no customer system is involved.

> **Echo's activation lapsed 2026-09-06 and is being renewed on the AEP1
> project** (see §10). Until it lands, S3, S4 and S2a run regardless; S1 and
> S2b are blocked. Sequence accordingly: **S3 and S4 first** — they are the
> spikes that actually gate the Tier B decision, and they need no controller.

- **S1 — Echo as a target.** *Blocked on an Echo activation.* Start a Logix
  Echo 5580 chassis, download any project, point `nautilus eip browse --host
  <echo>` and `nautilus eip import` at it. *Would prove the online plane and CI
  need no hardware.* Note this spike is **largely pre-proven**: `eip/` already
  works against a real Logix PLC in `examples/client60`, and `eip/logixserver`
  exercises the client side in tests. Echo's value here is CI, not
  feasibility.
- **S2 — SDK round trip.** Split it:
  - **S2a, runnable now, no controller:** `create_new_project` →
    `partial_import` a generated L5X routine → `build` → `save`. This is the
    half that de-risks codegen, and it needs nothing but Logix Designer.
  - **S2b, needs Echo or hardware:** `set_comm_path` → `download` →
    `go_online` → `set_tag_value(ONLINE)` → read it back over CIP with `eip/`.
    *Proves the project plane end to end, and proves the two planes agree.*
- **S3 — L5X stability. The riskiest cheap thing.** Upload from Echo twice with
  no change and diff. Then make a one-rung edit in Studio 5000, upload, diff.
  *Measures whether drift detection is clean or drowns in noise* — export
  dates, CDATA whitespace, attribute ordering, GUIDs. If L5X exports are not
  normalizable to a stable form, the git-native story needs rethinking. Write
  the normalizer here.
- **S4 — the 30 kB limit.** Generate a 200-rung routine L5X with `l5xgen` and
  partial-import it. *Determines whether generated code must be chunked*, which
  changes the emitter's shape.

### Phase 1 — `logixd`, the Windows agent

Python, job-queue HTTP, comm-path allowlist, split read/download tokens,
structured audit log, runs as a service. Jobs: `convert`, `upload`, `build`,
`download`, `mode`, `tag-get`, `tag-set`, `partial-import`. Ships with the
`OpenAndSaveFile` / `CreateNewProject` / `PartialImportOffline` work already on
the VM as its starting point.

### Phase 2 — `nautilus logix` CLI verbs (Go, talks to `logixd`)

- `nautilus logix convert` — ACD → L5X in bulk. Productizes the n26 work.
- `nautilus logix pull` — upload → normalize → workspace.
- `nautilus logix diff` / `--check` — drift gate for CI, mirroring
  `nautilus pull --check`.
- `nautilus logix download` — gated, confirmed, mode-checked.

### Phase 3 — `lang/l5x`, the reader (pure Go, no Windows)

RLL → `lang/ld` graph, ST → `lang/st` AST, UDTs → `stgen`, tags → tag file with
descriptions. Immediately lights up the ladder viewer, the FBD viewer, diagram
diffs between revisions, and hover — on Rockwell code. **Highest value per unit
of effort in the plan; start it in parallel with Phase 1 if there is capacity.**

### Phase 4 — `nautilus logix serve`, the facade

Six endpoints. Live from the online plane, `source` from the last normalized
pull, `editable: false`. The extension works unmodified.

### Phase 5 — Tier B spike, optional and explicitly time-boxed

LD → RLL first (it is the direction `l5xgen` already goes), over a **documented
subset** of the nautilus language. Gate it on a conformance harness that
downloads to Echo, drives both runtimes with the same inputs, and compares —
the only thing that makes generated code maintainable. If the harness is not
built, do not ship the emitter.

### Explicitly out of scope

FBD and SFC emission to L5X (sheet coordinates and wire routing), safety
(GuardLogix) anything, motion, and any attempt at online edits.

---

## 10. Open questions

- **Which Windows host is the production agent?** `rockwell-vm` is the lab.
  Pomona's EWS is a *customer* machine — anything that runs there is a
  deployment decision with its own authorization, not a development choice. The
  spikes should not touch it.
- **Is there a customer for Tier A, or is this a tool for our own delivery
  work first?** The answer changes whether `logixd` needs to be installable by
  someone else, which is most of its packaging cost.
- **Does the 30 kB operation limit apply to partial import?** (S4.)
- **Can L5X be normalized to a stable diff?** (S3. If no, §6.1 option 1 is
  weaker and option 2 carries more weight.)
- **Logix Echo licensing for CI — answered, and it is a blocker.** Measured on
  `rockwell-vm` 2026-09-20. Echo 4.00 and its whole controller catalog are
  installed, and an activation **did** exist: FactoryTalk Activation Manager
  lists *FactoryTalk Logix Echo Node*, serial **4260K18547**, feature
  `LGXNGEMU.SIM`, host `DESKTOP-07VCTIN` — **expired 2026-09-06** (support
  2026-09-07), shown greyed with an error marker. It lapsed 14 days before this
  was written.

  Everything else FTA serves is permanent: `fta.system`, `RSV.STUDIO`,
  `RSVME.STUDIO`, FT View SE/ME, RSLinx, the RSLogix 5/500/5000-era node-locked
  features, KEPServer, historian.

  **There is no grace period to preserve.** `RSsvr.log` (27 MB) holds 111,399
  `UNSUPPORTED: "LGXNGEMU.SIM" … No such feature exists. (-5,346)` denials and
  **zero** grace, evaluation or borrow records. Two things to keep straight,
  because they look alike in the log and are not:
  - FlexNet drops an *expired* feature from the served pool entirely, so the
    daemon then reports it as "no such feature" rather than "expired". The log
    line does not distinguish a lapsed activation from one that never existed;
    FTA Manager does.
  - FactoryTalk's 7-day grace covers an activation that became **unreachable**
    (server down, borrow lost). A **term activation reaching its end date** does
    not get grace — it is simply gone.

  **A VM rollback cannot recover it.** Term expiry is calendar-based against the
  host clock, so restoring a pre-expiry disk image does not rewind the date, and
  FTA carries clock-tamper detection. The incus snapshot
  `rockwell-vm/pre-echo-grace` (2026-09-20 15:10 PDT, ZFS CoW, instant) is
  harmless to keep but buys nothing here.

  **Path back:** a renewed or new activation against the Rockwell account tied
  to serial 4260K18547. A second 30-day trial on the same host should not be
  counted on — one was already consumed and lapsed on 09-06.

  **Decision (2026-09-20): renew Echo, funded by the AEP1 project, not by the
  lab.** Quoted at ~$2,200/yr. As lab overhead it is poor value — it would be
  the only subscription among otherwise permanent activations, and three of the
  four Phase 0 spikes do not need it. As a **delivery** cost on AEP1 it is
  justified, for three reasons a bench controller cannot answer:
  - **Full-size programs.** A used CompactLogix 5370 L1 (1769-L18ER-BB1B, the
    hardware alternative considered) has 512 KB of user memory. Staging a real
    converted program before a cutover is impossible on it. Echo has no such
    ceiling.
  - **The right processor family.** AEP1 targets redundant 1756-L81E — a
    ControlLogix 5580. Echo emulates 5580/5590/5380/GuardLogix at v33–v38; the
    L18ER is the previous generation at v30–v35.
  - **Several processor types from one activation** (up to 17 emulated
    controllers per license), which is also what a Tier B conformance harness
    would need later.

  The same activation then serves AEP1 delivery, the Phase 0 spikes, the Tier B
  harness and content recording — the multi-use case is what makes the number
  reasonable.

  **Open risk on that justification: does Echo emulate controller
  *redundancy*?** AEP1 is a redundant pair (this is why `l5xgen` forces
  periodic-only tasks and rejects local-chassis modules). Echo emulates
  multiple controllers in a virtual chassis, but redundancy requires a 1756-RM
  pair and it is not established that Echo reproduces it. **Confirm before
  purchase** — if it does not, Echo still stages the program on a single 5580,
  which is most of the value, but the redundancy behaviour stays untested until
  the real pair is available.

  **Timing:** the year-clock starts at purchase. If AEP1's staging phase is
  weeks out, buying then rather than now pushes coverage further into the
  nautilus Tier B window at no cost — unless a fiscal or PO constraint argues
  for buying while the budget exists.

  **Operational note, learned the hard way:** the previous activation lapsed
  unnoticed and cost a session to diagnose. Put the expiry date somewhere
  visible and set a reminder at ~11 months.

- **Bench hardware — downgraded to optional, not dropped.** With Echo funded,
  a used CompactLogix is no longer a blocker-remover. It retains two narrower
  uses: (a) validating `eip/` against a *real* Rockwell EtherNet/IP stack
  rather than an emulator — non-trivial for a driver we ship, given that
  `eip/driver.go`'s leaf mode exists because real AOI backing tags refuse
  struct-root reads with CIP 0x0F, and it is unproven that Echo reproduces
  that; and (b) physical I/O for demos and content, which an emulator cannot
  give. Note the *read* half of (a) is already covered by `examples/client60`
  against a real PLC; what is missing is a controller we are permitted to
  **download** to. Cheap, permanent, buy opportunistically.

  Side effect worth cleaning up: the `FactoryTalk Logix Echo Service` is set to
  Automatic and retries the checkout roughly every 10 s, which is the sole
  content of that 27 MB log and the reason it keeps growing. Stopping and
  disabling the service costs nothing until an activation exists.

  **Next step if Echo is wanted:** a 30-day trial activation from a Rockwell
  account, or a purchased activation. Until then, sequence the plan around
  S3/S4/S2a, which need no controller.
- **Is Logix Designer itself properly activated, or was August's work on
  grace?** No Logix Designer checkout appears in `RSsvr.log` either — the log
  is essentially pure Echo denial. The 52 headless ACD→L5X conversions
  demonstrably ran on 2026-08-22/28, so it works in practice; S3/S4 will
  confirm it immediately, and are the cheapest way to find out.
