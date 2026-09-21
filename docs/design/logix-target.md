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

> **⚠ Corrected 2026-09-20 — read `logix-sdk-api.md` §9 before relying on
> this section.** The premise below ("no online-edit API") is wrong. The SDK
> documents online partial import through
> `PartialImportWithTargetFromXmlFileAsync` and
> `PartialImportRungsFromXmlFileAsync`, whose `PartialImportOption` parameter
> is the test / accept / assemble workflow (`LeaveEdits`, `AcceptEdits`,
> `FinalizeEdits`). A *download* still stops the controller and resets tags —
> that part holds — but a rung- or component-level change can go into a
> running controller. Unverified against hardware; promoted to spike S2b.
> This section is left as written because reasoning from the overview table
> instead of the enum's own doc comments is a trap worth seeing.


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

> **Measured 2026-09-20: every SDK-dependent spike is blocked — but not for the
> reason first recorded here.** Logix Designer itself *is* licensed on
> `rockwell-vm` (feature `RS5K_700.EXE`, uncounted node-locked, permanent; the
> GUI opens v38 projects fine). **The SDK requests a different feature —
> `LDSDK.EXE` — and that entitlement is absent.** See §13. An earlier revision
> of this brief concluded "no valid Logix Designer license" from the SDK's
> error text alone; that was wrong, and the distinction matters because the fix
> is probably an entitlement request rather than a purchase.
>
> **What is still possible needs no Rockwell software at all**: L5X is a text
> format and there are ~60 exported L5X files (~30 MB) already on disk. S3's
> substantive question was answered from that corpus — see §11 — and **Phase 3,
> the L5X reader, is entirely unblocked.** That is now the thing to build while
> licensing is sorted.

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

---

## 11. S3 result — L5X normalizes well (measured 2026-09-20)

Run against the L5X already on disk, with no Rockwell software involved. Diff
counts are changed lines, `difflib` unified with zero context.

| Pair | Total lines | Raw diff | + attr-norm | + drop L5K |
|---|---:|---:|---:|---:|
| `DemoLine` vs `DemoLine.v80` — one setpoint changed, 85.0 → 80.0 | 14,488 | **4** | 4 | **2** |
| `AEP1_CLX` vs `AEP1_CLX_v37` — same project, Studio version upgrade | 6,387 | **6** | 6 | 6 |

**The format is far more diff-friendly than expected.** A single setpoint edit
surfaces as 4 changed lines in 14,488 — 0.03%. A *Studio version upgrade* of a
real project moves 6 lines in 6,387, which is the surprising one: version
migration is not diff-noisy.

**The volatile surface is tiny.** In 1.8 MB of `DemoLine.L5X`: one `ExportDate`,
one `LastModifiedDate`, one `DataExchangeId`, one `ProjectSN`, and exactly one
GUID-shaped value in the entire document. No timestamps sprinkled through
routines, no per-element identifiers. A normalizer is a handful of regexes, not
a canonicalizing XML rewriter.

**One real lever.** Every tag value is carried twice — once as
`<Data Format="L5K">` CDATA and once as `<Data Format="Decorated">`. Collapsing
the L5K copy halved the setpoint diff (4 → 2 lines). It is not a general 2×
win; it only applies to *value* changes (`DemoLine.L5X` holds 6 of each). The
same effect is reachable upstream through `ExportOptions`, which is the cleaner
place to do it if we control the export.

### Export determinism — MEASURED 2026-09-20, and it is clean

The decisive case has now been run, on **ECHO1** (see §13.5): the same
unchanged ACD exported twice through `OpenLogixProjectAsync` → `SaveAsAsync`.

```
conversion 1   Open succeeded · Save As succeeded   17.2 s   526,820 bytes
conversion 2   Open succeeded · Save As succeeded   11.0 s   526,820 bytes

differing lines: 2 of 13,194   (one line, both sides)
   => <RSLogix5000Content … ExportDate="Su…
   <= <RSLogix5000Content … ExportDate="Su…
```

**Identical byte-for-byte apart from the single `ExportDate` attribute.** Not
attribute ordering, not the `DataExchangeId` GUID, not CDATA whitespace, not
element ordering — one timestamp, on line 2, and nothing else in a 13k-line
document.

So the normalizer needed for drift detection is close to trivial: strip or pin
`ExportDate` (plus `LastModifiedDate`, which the earlier fixtures show also
moves) and two exports of an unchanged project hash the same. §6.1 option 1 —
workspace as source of truth, drift measured at the L5X level — is not merely
viable, it is exact.

**S3 is closed. The git-native story holds.**

**Verdict for planning purposes:** the git-native story is in good shape. §6.1's
option 1 (workspace as source of truth, drift measured at the L5X level) is
viable on this evidence, and the diff an engineer reads is genuinely reviewable
rather than a wall of noise.

*Spike script: `l5xnorm.py` in this session's scratchpad — ~40 lines, supersede
it with the Go normalizer that Phase 2 and Phase 3 both need.*

## 12. Where the licensed Studio 5000 actually is

Incidental but load-bearing for Phase 1. The ACD backups staged on the lab VM
are named `Pomona_RTU06v38.EWS2.admin.BAK000.acd` — Logix Designer stamps the
**host and user** into backup filenames, so those projects were last edited on
**EWS2**, the customer's engineering workstation, by `admin`.

Two consequences:

1. **The renewal has to cover Logix Designer, not just Echo.** A lab VM that can
   run Echo but cannot open an ACD is still blocked for every SDK spike.
2. **It sharpens §10's open question about the `logixd` host.** The one machine
   known to carry a working Logix Designer activation is a customer's. Running
   our agent there is a deployment decision with its own authorization, and it
   is not a substitute for a licensed machine of our own. Do not plan Phase 1
   around borrowing it.

---

## 13. The SDK is licensed separately from Logix Designer

Measured 2026-09-20, and it is the most consequential licensing fact in this
brief.

Running the converter produces `OperationFailedException: No valid license.`
— unhelpfully generic. Watching `RSsvr.log` during the attempt names the
actual request:

```
19:20:54 (flexsvr) UNSUPPORTED: "LDSDK.EXE" (PORT_AT_HOST_PLUS)
                   LOCAL SERVICE@DESKTOP-07VCTIN (No such feature exists. (-5,346))
```

Three distinct features, three distinct states:

| Product | FlexNet feature | State on `rockwell-vm` |
|---|---|---|
| Studio 5000 Logix Designer (GUI) | `RS5K_700.EXE` | **Licensed** — uncounted, node-locked, permanent |
| Logix Designer **SDK** | `LDSDK.EXE` | **Absent** — no such feature in any license file |
| FactoryTalk Logix Echo | `LGXNGEMU.SIM` | **Expired** 2026-09-06 |

**Diagnostic lesson worth keeping:** the SDK's error text says nothing about
which feature it wanted, and `flexsvr` reports both *absent* and *expired*
features as "No such feature exists". The only way to tell the three apart is
to watch `RSsvr.log` during the attempt and read the feature name, then check
FTA Manager for expiry. Any `logixd` health check should surface the feature
name, not the SDK's generic message — otherwise every licensing problem looks
identical in the field.

**Why this is probably good news.** Rockwell's guidance is that the SDK
requires a **Professional Edition** license or toolkit to activate, and that
the SDK entitlement must appear separately in FactoryTalk Activation Manager.
The `700` in `RS5K_700.EXE` is consistent with the 9324-RLD**700** Professional
catalog line — so the edition entitlement is likely already owned, and what is
missing is the SDK activation *on this host*. That is an account/support
request, plausibly at no cost, rather than a new purchase. Rockwell publishes a
dedicated answer for this exact error ("Logix Designer SDK: no valid license
found", answer 1139072) — start there.

**It also revises §7.2's first risk.** A Tier A deployment does not need "a
licensed Studio 5000" per machine; it needs **Studio 5000 Professional plus an
SDK entitlement** per machine — the top SKU, plus a second line item. Anyone
running `logixd` needs both. That is a materially worse cost structure for a
customer-installed agent than previously stated, and it strengthens the case
for keeping the *online plane* (pure-Go EtherNet/IP) free of any Rockwell
dependency, since that is the half a customer can deploy for nothing.

**Actions, in one conversation with the distributor:**
1. SDK entitlement (`LDSDK.EXE`) for `DESKTOP-07VCTIN` — confirm it is covered
   by the existing Professional license.
2. Echo renewal (`LGXNGEMU.SIM`) on AEP1 — see §10.
3. Ask what both look like for a *customer-installed* agent, since that is the
   Tier A deployment story.

### 13.1 It did work — what the evidence actually shows

The SDK unambiguously worked on this machine on the same build:

- `C:\acdwork\batch.log` ends `BATCH_DONE 2026-08-22T13:52:12 l5x=52` — 52 of 52
  ACD→L5X conversions, ~12 s each.
- Logix Designer SDK **2.02.00** and Studio 5000 v38.01 both have `InstallDate`
  **2026-07-09**, i.e. before that batch. Nothing was upgraded since.

So an `LDSDK.EXE` entitlement existed in August and is gone now. Two earlier
conclusions in this brief were drawn from `RSsvr.log` and were wrong because
**that log only begins `Start-Date: Thu Sep 03 2026 14:04`** — the VM's last
boot. It holds no August history at all, so "no checkouts in the log" meant
nothing.

`FTACmdUtility listAvailable` gives the authoritative current set: **29
activations, no `LDSDK.EXE`, no `LGXNGEMU.SIM`.** Note that `listAvailable`
lists only *available* activations — the expired Echo Node does not appear
there, though FTA Manager's GUI shows it greyed with its 2026-09-06 expiry. An
expired `LDSDK` entry would be hidden from the CLI the same way.

**Observation worth confirming before planning production on this host.** Of
those 29 activations, all but Echo's carry serials of the form `2650999999`,
`2529999999`, `2022999999` — placeholder serials — with round seat counts (10,
20, 750) spanning essentially the whole Rockwell catalog: RSLogix 5, 500, 5000
Professional, FT View SE/ME, ViewPoint, Historian, KEPServer, SoftLogix,
RSNetWorx, Batch. That is the signature of a **Rockwell demo/training
activation set**, not purchased seats. The single real-looking serial is Echo's
`4260K18547`.

If that is what this VM is, two things follow: the SDK's August entitlement
most likely came from the same time-limited grant that Echo's did and lapsed
with it; and **a demo activation set cannot underwrite a shipped Tier A
deployment** — the production licensing question in §7.2 is unanswered by
anything on this machine.

**Fastest confirmation:** scroll FTA Manager's activation list to where "Logix
Designer SDK" would sort (below "KEPServer Enterprise"). A greyed row with an
expiry date settles it in one second, and tells the distributor conversation
whether items 1 and 2 of §13 are one renewal or two.

### 13.2 The real cause: locked CodeMeter containers

Reinstalling the SDK component and rebooting did not help — `listAvailable`
still returns 29 rows with no SDK entry, and the converter still refuses. The
reboot *did* complete (WindowsUpdate and PendingFileRename flags cleared), so
the install is not the problem.

**This machine has two licensing systems running side by side**, and the
distinction explains everything observed so far:

| Mechanism | Holds | State |
|---|---|---|
| **FlexNet** (`flexsvr`, `.lic` files in `…\Activations`) | the legacy perpetual set — RSLogix 5/500/5000 Professional, FT View SE/ME, Historian, KEPServer, SoftLogix | **Working.** This is why Logix Designer opens v38 projects. |
| **CodeMeter** (WIBU, firm code `5000325` "Rockwell Automation, Inc.") | the modern entitlements — where `LDSDK.EXE` and `LGXNGEMU.SIM` live | **Broken.** |

`cmu --list` reports **16 CmContainers, 14 of them `(locked)`.** Only
`128-4551989` and `130-2465844567` are enabled.

That is the fault. A locked `CmActLicense` container cannot be read, so the
entitlements inside it are invisible to everything — which is precisely why
`LDSDK.EXE` and `LGXNGEMU.SIM` appear as "No such feature exists" to `flexsvr`
while nothing ever showed up as a `.lic` file: **they were never FlexNet
licenses at all.**

**Why containers lock, and why a VM is exposed to it.** `CmActLicense` binds to
a machine fingerprint. Restoring a snapshot, migrating the host, or changing
virtual hardware identity (disk, NIC, firmware) invalidates the binding and
locks the container. `rockwell-vm` was last started 2026-09-03 and Echo stopped
working on 2026-09-06 — suggestive, not proven, but the shape fits.

**This is recoverable and is probably not a purchase.** The remedy for locked
CmAct containers is re-activation against the Rockwell account that issued
them, not a new order. Check CodeMeter Control Center's WebAdmin
(`http://localhost:22350`) on the VM for each container's status and reason,
then re-activate through the Rockwell licensing portal. If the fingerprint did
change, it is a rehost.

**Consequences for the plan:**

- §13's "the SDK is licensed separately" stands — `LDSDK.EXE` is a distinct
  feature — but the earlier suggestion that it needs purchasing was premature.
  Fix the containers first.
- **`logixd` must treat CodeMeter as a first-class dependency.** Its health
  check needs to report container lock state alongside the FlexNet feature
  name; otherwise a fingerprint change in a customer's VM presents as the same
  opaque "No valid license" that cost this session several hours.
- **A virtualised agent host is a licensing risk, not just a convenience.**
  Anything in the Tier A deployment story that runs `logixd` in a VM inherits
  this failure mode. Worth stating plainly to customers, and worth preferring
  physical hosts or pinned VM identity where that is an option.

### 13.3 The healthy Rockwell container is on a different machine

A CodeMeter WebAdmin screenshot showing a green `Rockwell Automation Inc.`
container, serial `130-4270723735`, turned out **not to be from
`rockwell-vm`**. Checked directly: that serial appears neither in the VM's
`cmu --list` nor in the VM's own WebAdmin page, both of which list the same
fifteen `130-*` serials. (§13.2's conclusion was briefly withdrawn on the
assumption the screenshot was the same host; it is not, and §13.2 stands.)

State on `rockwell-vm`, confirmed:

- 16 CmContainers, **14 locked**. The locked ones are `130-*` version 3.00
  `CmActLicense` — the same family and version as the healthy Rockwell
  container in the screenshot, and they carry firm code `6000458`.
- The only *working* CodeMeter license is `128-4551989`, firm code `5000325`
  ("Rockwell Automation, Inc."), checked out by `FTAStub.dll` /
  `FTACommonEx.dll`. That is FactoryTalk Activation's own plumbing, **not a
  product entitlement**.
- The CodeMeter server search list is `255.255.255.255` — broadcast only. The
  VM discovers only itself on the incus bridge, so it cannot see a CodeMeter
  server on another host or subnet.

**Three routes forward**, depending on which machine holds the healthy
container and whether its licenses are network-enabled:

1. **Network licensing.** CodeMeter can serve licenses over the LAN. Adding
   that host to the VM's server search list (`cmu --add-server`) would let the
   VM borrow the entitlement with nothing moved or bought. Requires the host to
   be reachable — it is not on the incus bridge today, so this needs routing or
   a Tailscale address — and requires the licenses to be network-enabled rather
   than station-locked.
2. **Rehost / re-activate** the Rockwell containers onto the VM through the
   licensing portal.
3. **Run `logixd` on the machine that already works**, and treat the VM as a
   lab box only.

Route 1 is the interesting one for the *product*, not just this lab: it is the
supported way to give a build agent an entitlement without pinning a seat to
it, and it is the closest thing to a workable CI licensing story. Worth testing
deliberately if Tier A proceeds.

### 13.4 Settled: the CodeMeter container is healthy and empty

The Rockwell container that matters on `rockwell-vm` is `130-2465844567` —
the one `cmu` reported as *enabled*. WebAdmin shows it **green**, named
"Rockwell Automation Inc.", firm code **6000458**, and expanding its licenses
gives:

```
No Product Items available
```

Healthy container, zero licenses in it. That is the whole answer, and it
supersedes the locked-container theory in §13.2/§13.3 — the fifteen red
`<no name>` containers are noise, not the fault.

**Why everything else on the machine still works.** Two licensing systems, and
only one is broken:

- **FlexNet** holds the legacy perpetual set — RSLogix 5000 Professional
  (`RS5K_700.EXE`), FT View SE/ME, RSLinx, Historian, KEPServer. Node-locked,
  permanent, working. This is why Logix Designer opens v38 projects and why
  every other Rockwell application on the box is fine.
- **CodeMeter** holds the modern entitlements — the Logix Designer SDK and
  Logix Echo among them. Container present and healthy, **but empty.**

FactoryTalk Activation consults FlexNet, finds no `LDSDK.EXE`, consults
CodeMeter, finds no product items, and reports the uninformative
`No valid license.` Both halves of that search failing is why the error names
nothing.

**What most likely happened.** A time-limited Rockwell entitlement covering
the SDK and Echo was activated into this container, worked through the
2026-08-22 and 08-28 conversion runs, and **expired 2026-09-06** — the exact
date FTA Manager shows for the Echo Node. CodeMeter clears expired product
items, leaving the container intact and empty, which is precisely the state
observed. One event explains the SDK failing, Echo failing, nothing appearing
in the Activations folder, and all the perpetual software continuing to work.

**Therefore: this is a renewal, not a repair.** No rehost, no re-activation of
a broken binding, no separate SDK purchase to chase. The SDK and Echo came in
together and lapsed together, so one renewal should restore both — which also
means §10's AEP1-funded Echo decision should be scoped to cover the SDK
entitlement in the same transaction.

**One check that would confirm it:** on the machine whose Rockwell container is
populated, expand its Licenses and read the product items and their
`Valid Until` dates. If an SDK item is there with a live date, the comparison
is conclusive and also tells us whether network licensing (§13.3 route 1) is
even applicable.

**The durable lesson for `logixd`,** unchanged and now better founded: report
the FlexNet feature name *and* the CodeMeter container's product items. "No
valid license" with neither is what turned a five-minute diagnosis into a
multi-hour one.

### 13.5 ECHO1: the SDK runs, and the blocker is not licensing

`echo-vm` (hostname **ECHO1**, 10.154.92.210, incus description *"FactoryTalk
Logix Echo FAT rig — Pomona AEP"*) turns out to be a better SDK host than
`rockwell-vm`, and testing there moved the failure past licensing entirely.

Setup performed (2026-09-20):

- SSH as `windows` with `~/.ssh/echo_vm`; alias `echo1` added to `~/.ssh/config`.
- ECHO1 had the .NET **runtimes** 8.0.19 and 10.0.2 but **no SDK**, so the
  shipped examples could not be built. The SDK's C# examples target
  **net10.0** — .NET SDK 8 fails them with `NETSDK1045`. Installed .NET SDK
  **10.0.401** to `C:\dotnet10` via `dotnet-install.ps1` (user-dir, no admin).
- Built `OpenAndSaveFile` from
  `…\Logix Designer SDK\dotnet\Examples\src\OpenAndSaveFile` → `C:\s3build`.
  **Build succeeded.**

Running it against `Z:\AEP1_SIM.ACD` gives a *different* error from
`rockwell-vm` — no licensing complaint at all:

```
System.TimeoutException: The operation has timed out.
   at …FTSP.FactoryTalkServicesPlatformLogin.GetTokenForUserAsync(…)
   at …FactoryTalkServicesPlatformLogin.GetTokenForCurrentUserAsync(…)
   at …LogixProject.GetAuthToken(…)
```

**So the SDK authenticates against FactoryTalk Services Platform before it does
anything else.** That is a third dependency, alongside the FlexNet feature and
the CodeMeter container, and it is the one that bites first.

Diagnosis on ECHO1:

- FTSP 6.60.00 and FactoryTalk Activation Manager 5.02 are installed
  (2026-08-09); `RNADirectory` is running and listening on 4255 / 5241.
- No pending reboot; Studio 5000 v38.01 and SDK 2.02 installed 2026-09-20.
- **`HKLM:\SOFTWARE\WOW6432Node\Rockwell Software\FactoryTalk` contains only
  `Platform` — there is no `Directories` key.** The FactoryTalk **Local
  Directory was never configured** on this machine, so an FTSP login has
  nothing to answer it and times out.
- The fix is `C:\Program Files (x86)\Common Files\Rockwell\FTDConfigurationUtility.exe`,
  which is **GUI-only** (it hangs when driven headlessly with `/?`). RDP is
  closed on ECHO1, so it needs a console session (incus console) or RDP
  enabled.

**Why this matters well beyond this lab.** The SDK needs *three* things before
it will open a file: an FTSP auth token, a FlexNet feature (`LDSDK.EXE`), and a
CodeMeter entitlement. Each fails with a different and uninformative message,
and only the FTSP one is a pure configuration issue. Any `logixd` health check
must probe all three independently and say which is missing — this is now the
third distinct licensing/auth failure mode found in one session, and a customer
hitting any of them would see a generic error.

**Next step on ECHO1:** configure the FactoryTalk Local Directory from a
console session, then re-run `C:\s3build\OpenAndSaveFile.exe Z:\AEP1_SIM.ACD
C:\s3\run1.L5X false`. If it passes, S2a, S4 and the export-determinism half of
S3 all unblock on this host — and ECHO1, not `rockwell-vm`, becomes the SDK
box.

---

## 14. Handoff — picking this up in a fresh session

Written 2026-09-20 at the end of the analysis session. Everything below is
measured, not assumed; where something is inferred it says so.

### Where the work lives

- Branch **`logix-target`**, worktree `~/Development/joyautomation/nautilus-logix`.
  Nothing outside `docs/design/logix-target.md` has been touched — **no code
  written yet.**
- Content ideas **N-35…N-40** are committed in
  `~/Development/joyautomation/content/ideas.md`, with the harvest noted in its
  README.

### The verdict, in one paragraph

Runtime parity is unreachable (§6.2, §6.3: no online-edit API, so no warm
per-program download and no retained-state migration). **Tier A** — Logix stays
the runtime, nautilus supplies git-native source, review, CI, drift detection
and live monitoring — is high-feasibility and mostly built. **Tier B**
(code generation) is open-ended and should stay behind a conformance harness.
§8 is the recommendation; don't re-litigate it without new information.

### Machines — read this before touching anything

| Host | Access | State |
|---|---|---|
| **ECHO1** (`echo-vm`, 10.154.92.210) | `ssh echo1` (alias added; key `~/.ssh/echo_vm`, user `windows`) | **The SDK box.** Licensed, FactoryTalk Local Directory configured, .NET SDK 10.0.401 at `C:\dotnet10`, built `OpenAndSaveFile.exe` at `C:\s3build`. Logix Echo installed but its activation lapsed 2026-09-06. |
| **rockwell-vm** (`ssh rockwell`, 10.154.92.130) | working | **Not licensed for the SDK.** Its CodeMeter Rockwell container is healthy and *empty* (§13.4). Logix Designer's GUI works via FlexNet; the SDK does not. Don't burn time here. |

`Z:` on both VMs is the same host directory (`pomona/aep/conversion/output`).
Files there can be locked by a Logix Designer session on the *other* VM —
copy locally before converting, or make sure nothing has the ACD open.

### The gotcha that cost this session hours

**The SDK needs three independent things before it will open a file:** an FTSP
auth token, a FlexNet feature (`LDSDK.EXE`), and a CodeMeter entitlement. Each
fails with a different, uninformative message; on `rockwell-vm` licensing fails
first so you never see FTSP, and on ECHO1 FTSP failed first so you never saw
licensing. `flexsvr` additionally reports *absent* and *expired* features
identically. Diagnose by watching `RSsvr.log` during the attempt (it names the
feature), `FTACmdUtility listAvailable`, and `cmu --list-content`.

**`logixd` must probe all three and report which failed.** This is a product
requirement, not a lab note.

### What is ready to run right now on ECHO1

- **S2a** — `CreateNewProject` → `PartialImportOffline` → `BuildProject` →
  save. The codegen de-risker. All example projects are at
  `C:\Users\Public\Documents\Studio 5000\Logix Designer SDK\dotnet\Examples\src`;
  build them the way §13.5 describes (`C:\dotnet10\dotnet.exe build … -o C:\s3build`).
- **S4** — generate a large routine with `l5xgen` and partial-import it to find
  whether the documented 30 kB per-operation limit bites.
- Productizing the n26 batch conversion as `nautilus logix convert`.

S1 and S2b still need a controller — an Echo activation or bench hardware.

### What to build first, and why it needs none of the above

**`lang/l5x`, the reader** (§4.3, Phase 3). Pure Go, no Rockwell software, no
licensing. RLL routines → the existing `lang/ld` graph model, ST routines →
`lang/st` AST, UDTs → `stgen`, tags → a nautilus tag file *with descriptions*
(which a live browse cannot recover — `eip/codegen/tags.go:56`). It lights up
the ladder viewer, the FBD viewer, diagram diffs between git revisions, and
hover, on Rockwell code, with zero semantic-equivalence risk.

Corpus to develop against (~60 files, ~30 MB):

- `~/Development/joyautomation/content/assets/capture/n26/fixtures/` —
  `DemoLine.L5X`, `DemoLine.v80.L5X` (differ by one setpoint),
  `DemoProgram.L5X` (a *partial* export, `TargetType="Program"` — the shape a
  partial import takes, so it doubles as an emitter template). **Generic —
  safe as committed test fixtures.**
- `~/Development/pomona/wrd/docs/source/l5x/` (52 files) and
  `~/Development/pomona/aep/conversion/output/` — **client work, Tier 3.**
  Fine as local test input; never committed as fixtures, never on camera. Read
  `content/sourcing.md` before publishing anything derived from them.

Start from `lang/stgen` — it is the proven shape for this kind of code
(generate, then validate by compiling the output back through the parser) and
it is already used in production by `eip/codegen`.

### Decisions taken, so they don't get reopened

- **Echo renewal funded by AEP1, not the lab** (§10) — with the open question
  of whether Echo emulates 1756-RM redundancy, which AEP1 needs. Scope the
  renewal to cover the **SDK entitlement** too; an Echo node alone leaves you
  unable to open an ACD.
- **Bench hardware (1769-L18ER-BB1B) is optional, not a blocker** — kept for
  real-CIP-stack validation of `eip/` and physical I/O for demos.
- **Two planes, always** (§4) — the SDK for the project lifecycle, raw
  EtherNet/IP for anything at scan rate. Never route live data through the SDK.

### Corrections recorded deliberately

Three conclusions in this brief were wrong before they were right: reasoning
from `RSsvr.log` as though it were historical (it begins at the VM's last
boot), generalizing the SDK's "No valid license" to Logix Designer, and
assuming a CodeMeter screenshot came from the host under discussion. They are
left in §13.x rather than silently edited out, because each one is a trap the
next person would fall into the same way.

---

## 15. Never leaving VS Code, and CI-driven download

Raised 2026-09-20 and load-bearing for adoption: **if the workflow requires a
human to drive Studio 5000's File→Import, the product is dead.** Nobody will
alt-tab between VS Code and Logix Designer to ship a change.

### 15.1 The GUI is not required

It is not required, and that is precisely what the SDK is for. Every step of
the loop has an API:

| Step | SDK call | GUI equivalent being replaced |
|---|---|---|
| Make/choose the project | `create_new_project`, `open_logix_project` | File → New / Open |
| **Get generated code in** | **`partial_import_from_xml_file`** | **File → Import → Component** |
| Get code out | `partial_export_to_xml_file` | File → Export |
| Compile / verify | `build_project` | Verify Controller |
| Set the target | `set_communications_path` | Who Active |
| Ship it | `download` | Download |
| Read it back | `upload_project`, `upload_to_new_project` | Upload |
| Mode / online state | `change_controller_mode`, `go_online`, `read_connected_state` | the mode switch |
| Values, offline or online | `get_tag_value` / `set_tag_value` (`OperationMode.OFFLINE｜ONLINE`) | tag editor |

`partial_import_from_xml_file` takes an XPath and an L5X file and merges it into
the project with a collision policy — the SDK's own example addresses both a
whole program (`Controller/Programs/Program[@Name='MainProgram']`) and **a rung
range** (`…/RLLContent/Rung[@Number>='1'][@Number<='2']`). That is finer-grained
than most people drive the GUI.

**So the honest limitation is narrow** — and it is narrower still than this
paragraph claimed. ~~the SDK exposes no online-edit (test/assemble/accept)
API, so *changing a running controller without stopping it* still requires
Logix Designer.~~ **Wrong; corrected 2026-09-20.** The SDK documents online
partial import of rungs and of whole components, with `PartialImportOption`
supplying exactly test / accept / assemble. See `logix-sdk-api.md` §9.
What remains true is that a *download* stops the controller.

Everything else — author, generate, import, verify, download, upload, diff,
monitor, write values — is scriptable, and the monitoring half needs no
Rockwell software at all (§4.1).

That is a workflow a developer will actually adopt: **VS Code for authoring and
review, `logixd` for the project lifecycle, EtherNet/IP for live values, and
Studio 5000 only when someone genuinely needs an online edit.**

**Verification status, stated precisely.** The table above is drawn from the
SDK's shipped examples and the Getting Results Guide — authoritative about what
*exists*. Of those calls, this session has actually executed only
`open_logix_project` + `save_as` (§13.5, on ECHO1). `partial_import`, `build`
and `download` are **S2a/S2b and remain unrun.** Do not quote the table as
proven until they are.

### 15.2 Download from CI — feasible, with a real gate

Driving a download from GitHub Actions is mechanically straightforward: a
self-hosted runner on the Windows host, or any runner calling `logixd` over the
network. The hard part is not the plumbing, it is that **a download stops the
controller and resets tags to project values.** An unattended pipeline that can
do that to a live plant is a liability, not a feature.

Split the pipeline so the valuable half carries no risk:

**Always, on every PR — no controller involved, no risk:**
generate L5X → `partial_import` into a working copy → `build_project` →
report pass/fail. That is real CI for control logic: it catches what "it
compiled on my machine" never does, and it needs no plant, no downtime and no
permission. Most of the value lives here.

**Download — gated, and the gate should be layered:**

1. **Repo-side:** a GitHub Environment with required reviewers. Standard, and
   it makes the approval auditable next to the diff.
2. **Agent-side:** `logixd` holds its own allowlist of comm paths in local
   config, never caller-supplied. A pipeline cannot name a controller the
   operator has not already blessed.
3. **Plant-side permissive — the interesting one.** Before asking the SDK to do
   anything, `logixd` reads a permissive tag over EtherNet/IP (the online
   plane): `NAUTILUS_DOWNLOAD_PERMIT`, set by operators from the HMI, ideally
   self-clearing after a window. This is the same permissive pattern plants
   already use for every other consequential action, which means it needs no
   new concept to explain to operations — and it puts the final say on the
   plant floor, where it belongs, rather than in a merge button.
4. **Preconditions as code:** the same read path can assert process state —
   line stopped, no active alarms, mode as expected — before proceeding.
5. **Around the download:** `upload_to_new_project` first and archive it (a
   rollback artifact and a record of what was actually there), then download,
   then upload again and diff against what was pushed to prove it landed.

Steps 3–5 are where the two-plane architecture pays off twice: **the online
plane verifies the pre- and post-conditions while the project plane does the
work.** Neither half can do it alone.

**Open question for the procedure:** a download resets tags to project values.
Any real deployment needs an answer for tag-value preservation — Studio 5000
has its own upload/restore of tag values, and whether the SDK exposes enough to
automate it is unverified. Worth settling before the download path is offered
to anyone.

---

## 16. `lang/l5x` — built 2026-09-20

Phase 3's reader exists. Pure Go, no Rockwell software, no licence, no
Windows — exactly as §4.3 predicted, and it took one session.

### What it does

| Call | Out |
|---|---|
| `l5x.Parse` | the document model: UDTs, AOIs, tags (descriptions and operand comments included), programs, routines, rungs — each routine and rung carrying **the line it sits on in the L5X**, because the export is the source file here and the viewer addresses rungs by line |
| `l5x.Types` | the project's UDTs as IEC ST type declarations, via `lang/stgen` |
| `l5x.TagsYAML` | a nautilus tag file, **with the controller's own tag descriptions** |
| `l5x.Ladder` | the `lang/ld` render model — the same JSON `nautilus ld graph` emits |
| `l5x.Normalize` / `Equivalent` | the volatile attributes pinned, so two exports of unchanged code compare equal |

CLI: `nautilus logix import | graph | normalize | info`, structured like
`nautilus eip import`. `--check` on normalize is drift detection in one
command.

### Measured, on the corpus

- **55 files, 1,223 routines, 30,403 rungs — every rung parsed.** No
  failures, no special cases. The neutral text Logix exports is a small,
  regular language: instructions, `[leg , leg]` branches, a semicolon.
- **Every file's generated ST compiles**, which is the `stgen` pattern
  paying for itself — see below.
- `DemoLine` with every module-defined shape included: 252 KB of valid ST,
  10 unresolved names, all of them genuinely opaque firmware handles
  (`MODULE`, `REF_TO_AXIS_*`). Members of an unresolvable type are omitted
  and reported, never guessed.

### Three things the corpus taught that no spec would have

1. **Logix reserves no words.** Four of the 52 client files have a UDT
   member named `retain`; `DemoLine` has one named `Constant`. Both are
   IEC variable qualifiers, so both are parse errors in generated ST.
   They surfaced as *compile failures from `stgen.Render`*, not as bad
   files on disk — which is the entire argument for generate-then-compile.
   Identifiers now route through the ST lexer's own keyword table.
2. **A Logix UDT has no BOOL members.** An authored BOOL exports as a
   `Hidden="true"` SINT host plus a visible `BIT` member targeting it at a
   bit number. Rendering the type anyone actually authored means dropping
   the hosts and declaring the bits as BOOLs.
3. **Logix's predefined structures are not in the export.** `TIMER`,
   `COUNTER` and `CONTROL` are firmware-defined, so a file with a timer
   tag references a type it never declares. The reader carries their
   shapes; anything larger and mostly-opaque (`MESSAGE`, `PID`, the
   motion types) is reported unresolved instead of approximated.

### What it deliberately does not do

It renders. It does not execute, and it never claims a Logix rung and a
nautilus rung compute the same thing — which is why an instruction the
reader has never heard of still draws, as a box with its operands, the way
Studio 5000 draws it. **Zero semantic-equivalence risk was the point** (§8).

Still deferred, as scoped: ST routines → the `lang/st` AST (their text is
captured verbatim), FBD and SFC bodies (kept as raw XML rather than
dropped), and anything that WRITES L5X.

### The next step, and it is a real one

**`nautilus logix graph` emits the model; nothing in the editor consumes it
yet.** The VS Code extension threads `.ld` through ~20 places — a language
contribution, a custom editor, the diff commands, the active-file
tracking — and `.L5X` needs the same, plus read-only semantics, since an
L5X is not an editable nautilus source file. That is the work that turns
"the model is right" into "the ladder viewer and the revision diff run on
Allen-Bradley code", and it is a session of its own.

### Fixtures

`lang/l5x/testdata/README.md` says what each one is and why. The short
version: one verbatim partial export, one real pair differing by a single
setpoint (trimmed of the 1.8 MB of module-defined types that were almost
the whole file), and one hand-built file covering every shape the corpus
taught. The client corpus stays out of the repo and runs on demand:

    NAUTILUS_L5X_CORPUS=/path/to/exports go test ./lang/l5x/


---

## 17. The SDK's capabilities, surveyed — `logix-sdk-api.md`

Written 2026-09-20. A complete enumeration of Logix Designer SDK 2.02.00,
produced from the shipped XML documentation file, the installed Doxygen
reference, the release notes, and direct observation of the running service
on ECHO1 — not from memory. Every claim carries its source, and §11 of that
document says which of them have ever actually been executed (two).

What it settles that this brief had open or wrong:

- **§9 — the SDK supports online import.** `PartialImportWithTarget` and
  `PartialImportRungs` work online, and `PartialImportOption` is test /
  accept / assemble. §6.2 and §15.1 above are corrected in place. This is the
  most consequential finding and it promotes **S2b** to the top of the spike
  list.
- **ACD ↔ L5X is a documented two-call round trip.** `OpenLogixProjectAsync`
  accepts ACD, L5K *and* L5X; `SaveAsAsync` writes whichever the extension
  names. The 52-file headless conversion was this path, and `SaveAsAsync`'s
  `detailedL5x` flag is the upstream `ExportOptions` lever §11 wanted.
- **"~0.5 s per tag online" is in the vendor's own manual.** §4's two-plane
  split is conceded by the documentation, with a number.
- **There is no project browse.** `GetAllExecutables` and nothing else. Every
  other question about a project's contents is answered by exporting L5X —
  which makes `lang/l5x` (§16) structurally load-bearing rather than a
  convenience.
- **The 30 kB operation limit is real but the file requests are chunked**, so
  S4 is very likely a non-issue. Labelled as an inference, not a measurement.
- **The concurrency model is forced:** opens are serialized machine-wide,
  everything after that parallelizes, and one project's error can fail the
  others. That is `logixd`'s architecture, settled.
- **`BuildAsync` needs v37+**, which gates the "CI verifies the logic" half
  of the Tier A pitch.


---

## 18. `logixd` exists — and ECHO1 has no activation

Written 2026-09-21.

### What was built

`tools/logixd` — the Windows agent §4.2 called for, now real. A .NET 10
service that exposes the Studio 5000 SDK as JSON over HTTP, including
**both online-import endpoints** (§9 of `logix-sdk-api.md`):
`partial-import-with-target` and `import-rungs`, each taking the
`LeaveEdits` / `AcceptEdits` / `FinalizeEdits` option.

Alongside it:

- `logix/logixd` — the typed Go client. It preserves the SDK's own
  fatal/non-fatal error distinction and carries the SDK's event stream on
  every reply, because a failed import explains itself in the events and
  not in the exception.
- `nautilus logix probe | agent | convert | build | push | drift` — the
  agent-backed CLI verbs, alongside the pure-Go `import | graph | normalize
  | info` from §16. `push` is the online edit; `drift` is upload → convert →
  normalize → compare, which is §6.1 option 1 end to end.
- Unit tests for the client and the CLI (a fake agent), and a two-tier
  integration suite.

The agent also enforces what the SDK will not: serialized opens, one writer
per project path, a comm-path allowlist held agent-side rather than passed
in, and a file sandbox — because a remote caller has to get an L5X *to* the
machine before it can be imported, and an agent that reads any path it is
told to is a file server with a controller attached.

### What ran, and what did not

**Ran, against the live agent on ECHO1 from Linux:** health, bearer-token
rejection, the file round trip, work-directory escape attempts (all
refused), the probe report, and error classification. Six tests, all green.

**Did not run: anything that touches the SDK.** Not because of the code —
because **ECHO1 currently has no FactoryTalk activation at all.**

### What actually blocks it — corrected 2026-09-21, twice

**First conclusion, wrong:** "ECHO1 has no FactoryTalk activation, so the
SDK cannot run." The evidence looked airtight — `FTACmdUtility
listAvailable` reports none, there are no product `.lic` files, and
`RSsvr.log` shows `UNSUPPORTED: "LDSDK.EXE" … No such feature exists`.

**Then `OpenAndSaveFile.exe` ran, and succeeded.** DemoLine.ACD → L5X in
22.6 s. So the conclusion was wrong, and the measurement that breaks it is
worth stating precisely, because it overturns an assumption this brief has
carried since §13.1:

```
08:17:09  Open project started
08:17:11  (flexsvr) UNSUPPORTED: "LDSDK.EXE" … No such feature exists. (-5,346)
08:17:31  Open project succeeded
08:17:31  Save As succeeded
```

**The SDK asks for `LDSDK.EXE`, is refused, and does the work anyway.** For
open and save-as, the FlexNet feature is requested but **not enforced**. A
denial in `RSsvr.log` is therefore not, on its own, an explanation for
anything — and the whole ACD ↔ L5X half of the Tier A story may need no SDK
licence at all. That wants confirming deliberately before anyone relies on
it commercially, but it is what this host measures.

**Second conclusion, which the evidence supports:** the blocker is
**FactoryTalk authentication**, and it is configuration, not money.

Two gates behave differently, which is what finally separated them:

| Call | Needs an FTSP token | Result on ECHO1 |
|---|---|---|
| `GetProcessorTypesAsync(38)` | no | **works** — 106 processor types |
| `CreateNewProjectAsync(…)` | yes | `TimeoutException` at `FTSP.FactoryTalkServicesPlatformLogin.GetTokenForUserAsync` |

`HKLM\SOFTWARE\WOW6432Node\Rockwell Software\FactoryTalk` still has **no
`Directories` key**. The FactoryTalk Local Directory was never configured —
exactly the diagnosis §13.5 reached and ended on, whose fix was apparently
never applied. **§14's handoff table is wrong where it says ECHO1 has
"FactoryTalk Local Directory configured".**

It is also intermittent: the same `OpenAndSaveFile` run that succeeded at
08:17 failed at 08:35 with an unhandled .NET exception in 5.9 s, and the
FTSP timeout itself shrank from ~2 minutes to ~5 seconds between runs.
Consistent with a cached token that expired and could not be renewed.

**The fix**, from §13.5 and still not done: run
`C:\Program Files (x86)\Common Files\Rockwell\FTDConfigurationUtility.exe`
and configure the Local Directory. It is **GUI-only** — this session looked
for a silent or command-line switch and found none — so it needs an incus
console session or RDP enabled on ECHO1. Neither is drivable over SSH.

### What IS genuinely unlicensed

Echo. `LGXNGEMU.SIM` is denied once a minute, every minute
(`UNSUPPORTED … No such feature exists`), which matches the term activation
that lapsed 2026-09-06 on the other host. An `EmulateControlLogix5580`
process is nonetheless running and answering EtherNet/IP on
`100.93.56.45:44818` — `nautilus eip browse` connects and returns zero tags,
which is consistent with an empty controller and does not prove the
emulator will actually execute logic. **Whether an unactivated Echo node
runs a downloaded program is untested.**

### A theory that was wrong, recorded on purpose

The first timeout looked like a session-0 problem: `logixd` was detached
via WMI, FactoryTalk authentication plausibly wants an interactive logon,
and the symptom fit. So `logixd probe` was added as a CLI mode to run the
identical check from an interactive shell — and it produced the identical
timeout. **Session 0 was not the cause.** The probe mode stayed, because it
is the cheapest way to separate "the environment is wrong" from "the licence
is missing", and it earned its place by disproving its own reason for
existing.

### What this does not change

The online-edit finding (§9 of `logix-sdk-api.md`) still stands on the
vendor's documentation, and it is now *implemented and test-covered*:
`TestSDKOnlineRungImport` exports a routine's own rungs and imports them
back with `FinalizeEdits` while online — a semantic no-op over the real
online-edit path. It skips today with `live-create-project` named as the
failing gate. **The moment an activation lands, it runs with no edit.**

### What to do

1. **Configure the FactoryTalk Local Directory on ECHO1** —
   `FTDConfigurationUtility.exe` from a console or RDP session. Free, and it
   is the actual blocker. Then re-run `nautilus logix probe`.
2. Then run `go test ./logix/logixd/ -run TestSDK -v`. S2a is that command.
   S2b (the online edit) additionally needs a controller that will execute,
   which means Echo's activation after all — but only for S2b, and only once
   step 1 proves the SDK path works.
3. `.github/workflows/logix.yml` has the self-hosted job ready; flip the
   repo variable `LOGIX_SELF_HOSTED=true` once a runner labelled `logix-sdk`
   exists on the licensed box.


---

## 19. FactoryTalk authentication needs a logged-on desktop (2026-09-21)

The strongest hypothesis this session produced, and the one that explains
every observation. **Stated as a hypothesis, not a fact — the confirming
test has not been run.**

### The pattern

| Time | Console session | SDK call needing a token | Result |
|---|---|---|---|
| 08:17 | user driving Logix Designer | `Open` + `SaveAs` | **worked**, 22.6 s |
| 08:35 | user still on the box | `Open` + `SaveAs` ×2 | **worked**, 23.2 s / 12.2 s |
| 08:37 | user still on the box | `CreateNewProject` (Rockwell's example) | **worked**, 15.6 s, 3.8 MB ACD |
| 08:38–08:45 | session ending | everything needing a token | timeouts in `GetTokenForUserAsync` |
| 08:50 | **rebooted — no console logon at all** | `Open`, `CreateNewProject` | timeouts |
| throughout | — | `GetProcessorTypes` (**no token**) | **always works**, 106 types |

After the reboot there is no `explorer.exe` on the machine: nobody is
logged on at the console. Every call that needs a FactoryTalk token fails;
the one that does not need one is unaffected.

### Why this matters far more than it looks

**If it holds, `logixd` cannot run truly headless.** An agent started by a
service manager, by a scheduled task, or by WMI has no interactive desktop,
so FactoryTalk has no user session to mint a token for — and the whole
"CI runner drives the SDK unattended" half of §15.2 depends on solving
exactly that.

Possible answers, none yet tested:

1. Configure FactoryTalk for non-interactive authentication — a FactoryTalk
   user with stored credentials, or Single Sign-On disabled so the SDK
   authenticates explicitly rather than against the logged-on user.
2. Run the agent under an auto-logon console session (a logged-on kiosk
   user). Ugly, common in plants, and probably what most SDK users do
   without realising why.
3. Find whether `FtspAdapterLDSDK.exe` accepts explicit credentials. The
   client spawns it over a named pipe; running it by hand reports
   *"Invalid or missing pipe name argument."* and no spawn was ever observed
   during a failing login, which suggests the login fails before reaching
   it.

### How to confirm, in two minutes

Log on to ECHO1 at the console (incus console or RDP), leave the session
open, and run `nautilus logix probe`. If `create-project` flips to ok, the
hypothesis holds and option 1 above becomes the next piece of work.

The probe now reports an **`interactive-session`** gate for exactly this
reason, so the next person sees it rather than rediscovering it.

### What is NOT the cause — all ruled out by measurement

Licensing (`LDSDK.EXE` is denied even when calls succeed — §18), the
processor-type argument, the temp path, the `CancellationToken` overload,
the event logger, dependency versions, Server GC, running as `logixd.exe`
versus the `dotnet` host, stale server-side state, and a stale
`RSsvr.log`. Rockwell's own shipped example fails identically, which is
what finally moved the search off this codebase.
