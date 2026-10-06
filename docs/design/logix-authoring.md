# Design brief: author Allen-Bradley logic in nautilus, with Studio 5000 as middleware

Status: **direction set, nothing built.** Written 2026-10-03 on branch
`logix-authoring` (worktree `~/Development/joyautomation/nautilus-logix-authoring`).
This brief **supersedes the recommendation in [`logix-target.md`](logix-target.md)
§8** for the Allen-Bradley development use case. Everything else in that
document still stands, and most of what it built is reused here.

## 1. The thesis

> **Keep the hardware. Ditch the IDE.**

Plants will not replace a working fleet of ControlLogix and CompactLogix
controllers to get a better developer experience, and they should not have to.
They are more than ready to stop using Studio 5000 as the place where logic
is written. So for Allen-Bradley development, nautilus is the **only**
authoring surface:

- Logic is written, reviewed, diffed and tested in VS Code, as nautilus source
  in git.
- Studio 5000 runs **headless behind `logixd`**, as compiler, importer and
  loader. Nobody opens it to work. Opening it is only ever a way to confirm
  that the program in the controller is the one in the repo.
- Live values, set-value and the rung overlay come straight from the running
  controller over EtherNet/IP, the whole time.

Switching between Studio 5000 and VS Code is the experience we are removing,
so a workflow that needs both is not a partial win. It is the old workflow
with an extra tool.

What does **not** change: the nautilus runtime remains the product on its own,
and a nautilus controller working alongside an Allen-Bradley system (EtherNet/IP,
`driver: {type: eip}`) remains a supported and valued way to use it.

### What reverses, and why

`logix-target.md` §8 said: build the tooling around Studio projects (Tier A),
spike the writer (Tier B) behind it, never promise parity. The reason was cost:
generated code has to behave the same on two runtimes, the mapping is full of
decisions (§6.4 there), and those decisions must be maintained across Rockwell
versions indefinitely.

That cost is real, and this brief does not dispute it. The disagreement is
about whether it is affordable, and the answer here is that **it becomes
affordable once verification is cheap, automatic and run all the time** (§5).
An unverified writer is a liability. A writer that every change, every
instruction mapping and every supported firmware revision is checked against,
automatically, on an emulated controller, is maintainable.

Two parts of the old recommendation survive untouched:

- **No promise of runtime parity.** A Logix download still stops the
  controller; warm program swap with state migration is a nautilus runtime
  feature and stays one. We say so plainly.
- **Rejected constructs are rejected loudly.** The supported subset is
  documented and enforced at check time, not discovered at download time.

## 2. What already exists (reused, not rebuilt)

| Piece | State | Role here |
|---|---|---|
| `logixd` (Windows agent, SDK) | built | create project, partial import, rung import (incl. online edits), build, download, mode, upload |
| `naut logix` CLI verbs | built | probe, convert, build, push, download, drift |
| Online edit via SDK | verified on Echo 2026-09-21 (`logix-target.md` §20) | the "change a rung while running" path |
| `naut logix serve` | merged 2026-10-03 | live values, set-value, overlay from the controller |
| Rung overlay w/ program scope | merged 2026-10-03 | live values on the rungs nautilus wrote |
| `lang/l5x` reader + ladder render | built | verification (round trip, §5.2) and later brownfield import |
| `naut logix emulate` | built | tag-surface emulator; does **not** execute logic |
| FactoryTalk Logix Echo on ECHO1 | licensed lab | the logic-executing controller for verification |

## 3. What is missing

1. **The writer:** nautilus ladder source → Logix L5X (rungs, tags, types,
   program, task).
2. **`naut check --target logix`:** rejects what the writer cannot express,
   with errors that name the construct and the alternative.
3. **The deploy flow:** one command, and the editor's Download button, that
   generates, imports, builds, and then either online-edits or downloads.
4. **A home for hardware configuration** (§6, decision 1).
5. **The verification stack** (§5). This is not optional, and it is built
   alongside the writer, not after it.

## 4. The writer, v1 subset

Ladder first: it is the language Logix users expect to see in Studio, and the
nautilus LD model (`lang/ld`) keeps rung comments and structure. Emit from the
**graph/AST**, not the IR: the IR loses integer widths, comments and source
positions (`logix-target.md` §6.4).

| nautilus LD | Logix | Notes |
|---|---|---|
| `Name` / `/Name` contacts | `XIC` / `XIO` | |
| `[ a \| b ]` branches | `[ , ]` | nesting preserved |
| `( X )` `( S X )` `( R X )` | `OTE` / `OTL` / `OTU` | |
| `+Name` / `-Name` | `ONS` / `OSF` + generated storage BOOL | storage tag named deterministically, e.g. `_ons_<rung>_<n>` |
| `GT/GE/LT/LE/EQ/NE(a,b)` contacts | compare instructions | spelled as Logix **neutral text** spells them (`GE`, not the editor caption `GEQ`; see `logix-target.md` §21). The importer does not validate names, so tests must. |
| moves, arithmetic | `MOV`/math instructions | exact neutral-text mnemonics taken from the export corpus, never from the editor |
| `t:TON(PT := T#10S)` | `TON(t,10000,0)`, TIMER tag | references rewritten: `t.Q`→`t.DN`, `t.ET`→`t.ACC` (ms), `t.IN`→`t.EN` |
| `TOF` | `TOF`, TIMER tag | same rewrite; check the IEC/Logix reset behaviour difference in the harness |
| `c:CTU(PV := n)` | `CTU(c,n,0)`, COUNTER tag | `c.Q`→`c.DN`, `c.CV`→`c.ACC`; IEC `R` → `RES(c)` rung |
| BOOL, SINT, INT, DINT, REAL, LREAL | same | width from the declaration (AST), not the IR |
| `TIME` | DINT milliseconds | only where it feeds a timer preset; elsewhere rejected in v1 |
| global (`VAR_EXTERNAL`) | controller tag | |
| program-local `VAR` | program tag | |
| one nautilus task + program | one Logix task + program + `MainRoutine` | a stated decision, not a derivation |

**Rejected in v1, with a named error:** `TP`, `CTD`, `CTUD` (IEC load/reset
semantics differ from Logix), user FUNCTION / FUNCTION_BLOCK (later: AOIs),
ST routines (later: Logix ST is close to IEC ST, so this is v2), FBD, SFC,
STRING, arrays not starting at 0, multi-dimensional arrays, `CONTINUE`.

Names: Logix tag names are limited to 40 characters and must start with a
letter or underscore, which nautilus identifiers already satisfy except for length;
check-time enforcement.

## 5. Verification: the part that makes this maintainable

The objection to the writer was never "it can't be built". It was "it can't
be kept correct across instruction semantics and Rockwell versions". Every
layer below exists to turn that into a test failure instead of a field
failure. All of it runs without a human, and most of it without Windows.

### 5.1 Check-time (pure Go, every keystroke)

`naut check --target logix` runs the same subset rules the writer uses, so an
unsupported construct is a diagnostic in the editor, not a download failure.
One rule table, two consumers.

### 5.2 Structural round trip (pure Go, every PR)

For every test program: **nautilus source → writer → L5X → `lang/l5x` reader →
ladder graph**, and compare that graph with the source graph, modulo the
documented rewrites (timer members, edge storage tags). The reader already
exists and is corpus-tested against real exports, so a writer bug shows up as
a graph mismatch with no Rockwell software involved. Plus golden L5X
snapshots, normalized with `lang/l5x` normalize, so any change to emitted
output is visible in review.

### 5.3 Mnemonic and schema validation (pure Go, every PR)

Every instruction the writer can emit is checked against an allowlist built
from the **export corpus** (`lang/l5x/testdata`), because the SDK importer
accepts unknown mnemonics with `Errors="0"` and only build catches them
(`logix-target.md` §21 cost a month this way).

### 5.4 SDK import + build (logixd, every PR touching the writer)

Generate → `partial-import` into a fresh project → `build`. Build is measured
at under 200 ms on a v38 project (`logix-target.md` §20), so this is cheap
enough for every PR. Run on a self-hosted runner on the licensed Windows host,
or on any runner calling `logixd` over the network.

### 5.5 Behavioural equivalence (Echo, nightly and on writer changes)

The test that matters: **the same program, the same input sequence, on both
runtimes, and the same outputs.**

1. Run the nautilus program in the nautilus runtime (virtual time) with a
   scripted input trace; record outputs at each step.
2. Download the generated L5X to an Echo controller; drive the same input
   trace through `serve`/EtherNet/IP writes; sample the outputs.
3. Compare. Timer-dependent steps compare with a tolerance band derived from
   the scan and poll period, because Echo runs on a real clock.

Two sources of programs:
- **One conformance program per mapped instruction**, written to probe its
  edges (timer reset while timing, counter at preset, edge on the first scan,
  compare with NaN where it applies).
- **Generated programs:** random ladder over the v1 subset (property-based),
  so combinations nobody thought to write get tested. Failures shrink to a
  minimal rung.

### 5.6 Version matrix (scheduled)

Echo 4.00 was recorded with 5580 firmware v33–v38 and 5590 v38 installed on
**rockwell-vm** (content idea N-38). Check what ECHO1 has before planning the
matrix there. Run 5.4 and 5.5 across every installed revision on a schedule, so
a new Studio 5000 or firmware release is a red build, not a support call.
Supported revisions are whatever the matrix proves, published as such.

### 5.7 Deploy-time checks (every download)

From `logix-target.md` §15.2, unchanged: upload and archive before, download,
upload after and compare with what was sent. Drift (`naut logix drift`) needs
a **logic-only mode** for this, because today it compares tag values too,
and a running controller's values always move.

## 6. Decisions to make

1. **Hardware configuration.** Controller model, firmware revision, chassis
   and I/O modules have to live somewhere. Proposal: `logix/hardware.L5X` in
   the repo, captured once from an existing project (or created by `logixd`
   for a bare controller), and merged by the writer. It is the one
   Studio-shaped artifact left; generating it from YAML is a later phase.
2. **Online edit or download.** Rung-only changes go as an online edit
   (verified path). New tags or structural changes need a download, which
   stops the controller and so needs explicit confirmation. Unverified: how
   much the SDK allows online beyond rungs (tags added online, for example).
3. **Project layout.** A nautilus project gains a `target: logix` (controller,
   comm path, firmware revision) so `naut check`, the editor's Download
   button and the deploy command all know what they are aiming at.
4. **Tag-value preservation across downloads.** Open since `logix-target.md`
   §15.2. Needed before anyone downloads to a plant.
5. **Names for generated tags** (edge storage, rewritten timers) must be
   stable across regenerations, or every regeneration looks like drift.

## 7. Phases

- **Phase A — writer v1 + check target + structural tests (5.1–5.3).**
  Pure Go. Done when DemoLine's two rungs, written as nautilus LD, round-trip
  through L5X to the same graph.
- **Phase B — deploy flow (5.4, 5.7).** `naut logix deploy` (name open) and
  the editor's Download button for `target: logix`: generate, import, build,
  online-edit or download, verify. Done when a rung edit in VS Code lands as
  an online edit on Echo with live values on throughout.
- **Phase C — behavioural harness (5.5) and version matrix (5.6).**
  Gates calling the writer a product. The demo can be recorded at the end of
  Phase B on the documented subset; it cannot be sold before Phase C.
- **Phase D — widen the subset.** ST routines, then FBs as AOIs.
- **Later — brownfield.** Existing Logix ladder → editable nautilus source,
  using the existing reader. This is where most of the market is (every plant
  already has the code), and it is what makes "ditch the IDE" possible for a
  site that did not start in nautilus.

Rough effort, not a commitment: Phase A about a week, Phase B about a week,
Phase C about a week.

## 7a. Kill criteria: when this is untenable

This is an **experimental proof of concept**. It earns the next phase only by
passing the criteria below, checked at the end of each phase and recorded in
this document (§7b, a dated log). Each criterion is a measurement, not an
impression. On a hit the outcome is one of three, and James decides which:
**continue**, **re-scope** (narrow the subset or the promise), or **stop**.

Stopping is not zero. The Tier A tooling (L5X view, live values, drift, CI
build) and nautilus-alongside-Logix stay, and that is the fallback product.

### Stop: the premise fails

These end the pursuit, because the promise is "never open Studio 5000":

- **Any step of the §8 demo loop needs the Studio 5000 GUI** (a dialog, a
  manual verify, a hardware-config step that cannot be carried as a repo file
  and merged headlessly) and the SDK offers no way around it.
- **The SDK cannot online-edit rungs the writer generated**, only rungs
  Studio exported. The demo's core move would then require Studio.
- **Rockwell's licence terms prohibit this use** of the SDK (headless,
  agent/server, CI). Check the SDK EULA in Phase A; it costs an hour.

### Re-scope or stop: the cost is as high as §8 of `logix-target.md` feared

- **Special cases instead of rules.** The writer needs a per-program fix, or
  more than a handful of per-firmware-revision branches, to pass its tests.
  Rules scale; special cases are the maintenance cost the old recommendation
  predicted.
- **The equivalent subset is too small to be useful.** After Phase C, the
  instructions that pass behavioural equivalence cannot express a realistic
  program. The yardstick is batch-skid's `line/Line.L5X` logic: if it cannot be
  written in the passing subset, the subset is a toy.
- **An instruction cannot be made equivalent and cannot be cleanly
  rejected**, i.e. it is common in real programs and its Logix semantics
  differ in a way no rewrite or check-time rule closes.
- **Verification is not trustworthy.** The behavioural harness flakes on more
  than 2% of runs after a week of fixing, or Echo/logixd failures (licensing,
  tokens, agent crashes; see `logix-target.md` §19) interrupt more than one
  scheduled run in five. A verification layer that cries wolf gets ignored,
  and then the writer is unverified.

### Re-scope: the developer experience is not better than Studio

The point is a better dev loop. If it is slower than the tool it replaces,
nobody uses it:

- **Edit → running on the controller as an online edit takes more than 30 s**
  (save in VS Code to rung live on Echo), median over 20 edits.
- **A full generate → import → build → download takes more than 2 minutes** on
  DemoLine-sized projects.
- **Errors surface late.** More than an occasional failure first appears at
  SDK import or build rather than in `naut check --target logix`. Every such
  failure should become a check-time rule; if they keep coming, the rule set
  is not converging.

### Timebox

Any phase running past **twice its estimate** (§7) stops for a review with
James before more time goes in, whatever the criteria above say.

## 7b. Phase log

Record each phase-end check here: date, each criterion measured with its
number, and James's call (continue / re-scope / stop).

### Phase A — 2026-10-03

Built: `logix/writer` (the LD → L5X writer and its rule table), `naut
check --target logix`, `naut logix write`, and the §5.1–5.3 verification:
DemoLine written as nautilus ladder produces **the same rung text, byte for
byte**, as Studio 5000's export of the same logic (`lang/l5x/testdata/
demoline.L5X`); a structural round trip through the `lang/l5x` reader;
golden L5X snapshots; and an instruction-name allowlist from the export
corpus. Estimate was about a week; it took one session. Timebox not hit.

**Stop criteria**

- *Studio GUI needed in the demo loop* — not measurable until Phase B
  (nothing in Phase A touches the SDK). Untested.
- *SDK cannot online-edit generated rungs* — untested (Phase B). The
  writer's rung text being byte-identical to Studio's own export removes
  the most likely cause of a refusal.
- *Licence terms prohibit this use* — **checked; no prohibition found, two
  clauses need a written answer before this is sold.** Read: the Rockwell
  Automation End User License Agreement (Rev 7/2019, `Rockwell-EULA-and-
  Addendum_English-Final2019.pdf`) and the current Software and Cloud
  Services Agreement (Rev April 2026, rockwellautomation.com → legal
  notices), which the SDK's own guide (LDSDK-GR001) points to. Neither
  prohibits headless, unattended, service or CI use. The clauses that
  matter, quoted from the April 2026 agreement:
  - §1.4.10 (2019 §4.3.d): "You may not use the automation interface or
    other programmatic interfaces contained within the Software in
    conjunction with any third-party software not authorized by Rockwell
    Automation in writing, including, but not limited to, change
    management systems." The Logix Designer SDK is a separately licensed
    product whose documented purpose is "programmatic access for
    third-party applications" (LDSDK-GR001), so the SDK licence is the
    written authorization for the licensee's own tooling. The clause names
    change-management systems, and nautilus-plus-`logixd` is one. **Get a
    reseller or Rockwell confirmation in writing before selling.**
  - §1.4.7 (2019 §4.3.b): no pooling or multiplexing "to reduce the number
    of required licenses that directly access or use the Software." One
    `logixd` serving many nautilus developers from one SDK activation is
    this. Mitigation: one SDK activation per concurrent `logixd` user, and
    say so in the docs.
  - §3.4.1 (2019 §4.2.a): no hosting "as an application service provider
    or the like for other third parties." `logixd` runs on the customer's
    own licensed machine, never as a JoyAutomation-hosted service.
  - §1.3.1: the grant is for "Your own internal business purposes" — the
    licensee is the plant; nautilus is their tool. Fine.
  - Not read: the SDK's own `license.rtf` in its install folder on ECHO1.
    It needs a read over ssh (James's call; the rule is ask before touching
    Echo) or James can paste it.

**Re-scope / stop criteria**

- *Special cases instead of rules* — **0** per-program fixes, **0**
  per-firmware branches. The mapping is 17 rules (`rules.go`) and one
  positional exception (a rising edge at the head of a rung inlines as
  `ONS`; anywhere else it is a helper rung, because `ONS`/`OSR`/`OSF` act
  on the rung-in condition).
- *Subset vs the yardstick* (batch-skid `line/Line.L5X`) — assessed after
  Phase C; measured now for the trend. 5 rungs, 12 instruction uses, 7
  mnemonics: `XIC XIO OTE OTU TON GE LES`. **11 of 12 uses (6 of 7
  mnemonics) are in the emit set.** The seventh, `LES`, is the editor
  caption, not neutral text (§21 of `logix-target.md`: it is `LT`), so
  that fixture would fail an SDK build exactly as DemoProgram's `GEQ` did —
  worth a one-token fix in `examples/`. The real gap is the data model:
  `Line.L5X` has one UDT (`Line_Status`) and a coil on its member, and v1
  has no UDTs. **Instruction set: covered. Types: not yet.**
- *An instruction not equivalent and not rejectable* — none in v1. The two
  semantic differences found were closed with rules, not special cases: a
  Logix timer/counter passes rung-in through, so power continues as
  `XIC(t.DN)` (and a `TOF`, whose DN can be true with its input false,
  ends its rung and the rest moves to a continuation rung); a one-shot
  acts on the rung-in condition, so an edge away from the rung head takes
  a helper rung. `OSF`'s storage bit is the previous rung-in, initially 0,
  so a falling edge does not fire on the first scan — the same as
  nautilus's `F_TRIG`. Open for the harness: compares with NaN, a `TOF`
  reset while timing, counter overflow.
- *Verification trustworthy* — pure-Go suite: 14 writer tests + 3 CLI
  tests, **25 consecutive runs, 0 failures**, 0.3 s. No Echo or `logixd`
  runs yet, so the flake and interruption rates are unmeasured.

**DX criteria** — edit → online edit: Phase B. `naut logix write` and
`naut check --target logix` on DemoLine: **under 10 ms** each (0.00 s
wall). Errors surfacing at SDK import/build: **0 of 0** attempts; the
first SDK build is Phase B's first test.

**Decisions taken in Phase A**, recorded so they don't get reopened:

1. The writer emits a **controller-target** export, not a Program partial
   export: a partial import cannot create controller tags (`VAR_EXTERNAL`),
   and `naut logix convert` already turns a controller L5X into an ACD.
   Phase B's online path uses `import-rungs`, which takes the per-rung
   text the writer already produces.
2. A Logix rung comment is the `//` note run directly above the rung plus
   the header's `(* … *)` text.
3. The "moves, arithmetic → MOV/math" row of §4 is vacuous for ladder:
   nautilus LD has no move or arithmetic element (a function contact must
   yield BOOL). `MOVE` is emitted only for a variable preset
   (`PT := tvar` → a helper rung `MOVE(tvar,t.PRE)`, `tvar` carried as DINT
   milliseconds). Arithmetic arrives with ST routines (Phase D).
4. `( P X )` / `( N X )` coils are rejected in v1 (they are not in the §4
   table); `+Name` / `-Name` contacts are the edge form.
5. Generated tags mirror `lang/ld`'s implicit edge-instance names
   (`rt_<rung>_<ref>`, `ft_…`, output bit `…_Q`): stable across
   regenerations and the same string on both runtimes (§6, decision 5).
6. `ExportDate` defaults to `(pinned)`, so regenerating unchanged logic is
   byte-identical and `naut logix normalize --check` compares equal.

**Assumptions Phase B's first SDK import + build retires:** `OSR`/`OSF`
operand order (StorageBit, OutputBit — from the instruction reference);
the L5K spelling `[0,PRE,0]` for TIMER and COUNTER; the hand-written
envelope (`Owner="nautilus"`, the `Local` module block); the Decorated BOOL
array form; `MOVE` into a `.PRE` member.

**James's call (2026-10-03): continue to Phase B.**

### Phase B — in progress (2026-10-03)

**ECHO1 was a fresh VM** (created 2026-09-27, rebooted 09:04 with
"before-grace" snapshots): Studio 5000 v38.01 and SDK 2.02 installed and
the SDK service up, but no .NET SDK, no `logixd`, no Echo controller, no
FactoryTalk activation listed. Set up again, all reversible:

- .NET 10.0.401 x64 SDK at `C:\dotnet10` (dotnet-install, `-NoPath`).
- `logixd` from `tools/logixd` via `install.ps1 -Listen 0.0.0.0 -Port 8188
  -DotnetRoot C:\dotnet10`; every probe gate green, including
  create-project. Token at `C:\ProgramData\logixd\logixd.token`; a copy for
  nautilus lives OUTSIDE the repo in `~/.config/nautilus/logixd.env`.
- The installer's firewall rule covers Private/Domain profiles only and the
  VM's NIC is Public, so port 8188 is unreachable from the LAN. Rather than
  widen the rule, nautilus reaches the agent over an SSH local forward:
  `ssh -f -N -L 18188:127.0.0.1:8188 echo1`, URL `http://127.0.0.1:18188`.
  James decides whether the rule should change.
- `tools/logixd` is also staged on the Y: share as `Y:\logixd`.

**§5.4 measured:** both fixtures (DemoLine, the full v1 subset) convert to
an ACD and build on the real SDK: `TestSDKConvertAndBuild`, 26 s per fixture
end to end, build itself under 1 s warm (5–9 s on the first build after the
service starts). One import warning, `ShareUnusedTimeSlice`, fixed by
mirroring the L85E export. **Every assumption Phase A listed is retired:**
`OSR`/`OSF` operand order, the L5K `[0,PRE,0]` spelling, the envelope, the
BOOL array form, `MOVE` into `.PRE` — all accepted by import and build.

**Built so far:** `WriteRungs` (the Rung-target partial export an online
edit sends — shape taken from an SDK partial export of a rung);
`target: logix` in `nautilus.yaml` (+ schema), which makes `naut check` run
the writer's rules unasked; `naut logix drift --logic` (§5.7's logic-only
mode: programs, routines, rung text, tag shapes, never values);
`naut logix deploy` — write, import, build, then upload the running
controller and decide online edit vs download from the logic diff, do the
one asked, upload again and verify. Build-only deploy of the DemoLine
project fixture (`logix/writer/testdata/project`) measured at **31 s**
end to end through the tunnel (the SDK open/convert dominates).

**Also built:** `logix/deploy` (the flow as a package, shared by the CLI
verb and the facade) and the editor's Download button: `naut logix serve
--project <dir>` turns on a program plane where GET /api/program serves the
task's ladder source and PUT /api/program runs the deploy as an online
edit; a change that needs a download is refused with the command that does
it. Tested against a fake agent and the in-repo emulator.

**On the controller (2026-10-03, James created it):** Echo 5580 v38 in
slot 0, bound to the VM's Tailscale address; comm path
`AB_ETH-1\100.93.56.45\Backplane\0`, found by probing slots over
EtherNet/IP and reading FT Linx's driver list (no device had been browsed;
the path resolves anyway). An upload from a never-downloaded controller
fails with `RxE_NOT_FOUND`; deploy now reads that as "first download".
**`naut logix deploy --download --yes` of DemoLine: 1 m 37 s** from command
to verified (write, import + build 29 s, download, upload, logic compare),
under the 2-minute §7a limit. Tags visible over EtherNet/IP afterwards.
Studio 5000 will not open an ACD from the Y: virtiofs share ("does not
exist"); copy to C: first.

**Live values against the real controller:** `naut logix serve --project
logix/writer/testdata/project` browses 6 tags over EtherNet/IP at the
Tailscale address, serves them (HiLevelSP reads 85, the writer's initial
value), and GET /api/program answers with the ladder source, `language:
ld`, a hash and `editable: true` — the extension needs nothing new.

**Online edit of writer-generated rungs (2026-10-03):** a one-contact
change (`/HiLevelAlm` added to the seal rung) on a scratch copy of the
fixture, `naut logix deploy --online`: both rungs replaced in
MainProgram/MainRoutine with `FinalizeEdits`, controller mode unchanged
(Program), verified by upload. **The §7a premise criterion "the SDK cannot
online-edit generated rungs" is cleared.** Measured **2 m 23 s** from
command to verified: import + build 35 s, then upload-before, the online
session (open, comm path, go online, import) and upload-after at roughly
30 s each, because every SDK project open costs 15–20 s. The §7a DX target
is 30 s median save → live. The warm path is the facade holding one
correlated project open and online across edits, so an edit is one
`ImportRungs` (1.1 s in logix-target.md §20) plus a routine partial export
to verify. Not built yet; the cold path is what `naut logix deploy` does.

**The warm path, measured (2026-10-03):** `logix/deploy.Session` keeps one
project uploaded from the controller open and online; an edit is one
`ImportRungs` with `FinalizeEdits` plus a partial export of the routine to
verify. Driven through `naut logix serve --project` with the exact
`PUT /api/program {source, baseHash}` the extension sends:

| edit | wall time | result |
|---|---|---|
| first after start (opens the session: upload, open, go online) | 62.9 s | 2 rungs replaced, verified |
| second | 2.1 s | 2 rungs replaced, verified |
| third | 2.1 s | 2 rungs replaced, verified |

**§7a DX criterion (edit → live on the controller under 30 s): met on the
warm path at about 2 s**, with a one-time session cost at startup. The
stale-base 409 and the refusals (tags changed → "needs a download", a
construct outside the subset) are what the extension shows verbatim.

### Phase B — check (2026-10-03)

Estimate about a week; one session (same day as Phase A). Timebox not hit.

**Stop criteria**
- *Studio GUI needed in the demo loop:* **no.** Create (writer), download,
  online edit and live values all ran headless through logixd and
  EtherNet/IP. One-time commissioning outside Studio: FactoryTalk Linx had
  no device browsed, and the comm path resolved anyway.
- *SDK cannot online-edit generated rungs:* **cleared** — writer-generated
  rungs imported online with `FinalizeEdits`, controller mode unchanged,
  verified by export, four times.
- *Licence:* unchanged from Phase A; still owed a reseller confirmation.

**Re-scope / stop criteria**
- *Special cases:* **0** per-program fixes, **0** per-firmware branches.
  One new rule from an SDK message (`ShareUnusedTimeSlice`), fixed by
  mirroring the export, and one new behaviour (an empty controller is a
  first download, not an error).
- *Errors surfacing late:* **1 of 2** first-time SDK imports warned on
  something the writer should have known (the time-slice attribute); none
  since. Zero build failures across every SDK build run.
- *Verification trustworthy:* every real-controller deploy verified; no
  flake seen across 1 download, 1 cold online edit, 3 warm online edits and
  3 SDK builds. Too few runs to quote a rate; Phase C's harness measures it.

**DX criteria**
- Edit → live, warm path: **~2 s** (3 edits). Cold path (`naut logix
  deploy --online`): 2 m 23 s.
- Full generate → import → build → download: **1 m 37 s** (target 2 min).

**Done?** The definition was "a rung edit in VS Code lands as an online
edit on Echo with live values on throughout." The edit went through the
extension's own API call, not a click in VS Code, with live values served
throughout; the controller was in Program mode (Run is James's to set).
Left for later phases: `logix/hardware.L5X` merge (§6.1) and tag-value
preservation across downloads (§6.4), neither needed for the demo.

**James's call (2026-10-03):** continue; and the harness takes the shape
James proposed — the project's own `*_test.yaml` scenarios, run as unit
tests on the nautilus runtime and again, after the download, against the
controller over EtherNet/IP.

### Phase C — in progress (2026-10-03)

**`naut test --target logix` built and measured.** `acceptance.Live` runs
the same scenario files through the facade's mirror of the controller:
`given` writes wait for read-back, `advance`/`scans` are wall time plus a
poll, `until`/`hold`/`always` evaluate every poll, `suspend` is a no-op
(the tasks it names are nautilus tasks). The writer now carries manifest
seeds and descriptions onto controller tags, and the DemoLine fixture
declares its field tags in `nautilus.yaml` with the program taking them
`VAR_EXTERNAL`, which is the ordinary nautilus shape and makes the Logix
tags controller-scope.

Measured on ECHO1, same file both layers:

| layer | result |
|---|---|
| `naut test` (nautilus runtime, virtual time) | 2 passed, 0.4 s / 0.3 s |
| download of the project-tag DemoLine (tag set changed) | 2 m 19 s, verified |
| `naut test --target logix` (Echo in Run, 100 ms poll) | 2 passed, 1.25 s / 0.92 s |

That is the whole claim of the brief in one command pair: the program
proven in nautilus, then the download proven with the controller as the
runtime.

**Side code (James, 2026-10-03):** logic nautilus adds beside the user's
program for its own purposes — testing, verification, metrics — lives in
a Logix program of its own, `Nautilus`, scheduled after the user's in the
same task, so the user's routine in Studio 5000 is their source and
nothing else. First piece: a heartbeat, `target.logix.side.heartbeat`
naming a controller DINT the side program increments every task scan
(`ADD(Nautilus_Scan,1,Nautilus_Scan)`). `naut test --target logix` waits
on it, so `scans: n` is exactly n controller scans, and a counter that
stops is reported as "is the controller in Run?". Downloaded to Echo
(2 m 20 s, verified: the new program and tag are the whole logic diff);
the scenarios pass with scan counts reported from the controller.

**Conformance projects (2026-10-03):** `logix/writer/testdata/conformance/`
— timers, counters, edges, logic — one ordinary nautilus project per
instruction family, each with a `target: logix` section and scenarios
that assert only what EtherNet/IP can see (edges latch what they saw; a
0.5 s margin around every 2 s preset). Writing the counter scenarios
found a writer bug on paper before it ran anywhere: a Logix CTU's done
bit stays true at the preset with no pulse present, so an inline
`XIC(c.DN)` after the CTU would have made Done = Pulse AND DN. A CTU now
ends its rung like a TOF; the rule table says so.

All four, each downloaded to Echo (2 m 16 – 2 m 23 s, verified), put in
Run, and run against the controller with the heartbeat:

| project | scenarios | nautilus | Echo (controller as runtime) |
|---|---|---|---|
| timers (TON/TOF: delay, reset while timing, re-arm during the off delay) | 4 | pass | pass, 3.1–4.9 s each |
| counters (CTU: edge counting, held pulse, reset, done outliving the pulse, gated) | 3 | pass | pass, 2.2–2.6 s |
| edges (+/− at the head and after a contact; the edge is the tag's own, not the rung's) | 2 | pass | pass, 1.3–2.0 s |
| logic (series, parallel, nested, compares and a negated compare, latch, two coils) | 4 | pass | pass, 0.9–1.4 s |

**13 of 13 pass on both runtimes.** The scan counts reported are
heartbeat counts: a `scans: 2` step spends at least 2 controller scans
and then one poll, and a 10 ms task runs about ten scans per 100 ms poll,
so a step reads as ~10 scans. That is the semantics (at least n), stated.

**Version matrix — a constraint found, not a matrix run.** ECHO1 has Echo
firmware v33–v38 for the 5580 installed, but only Logix Designer v38.01.
The SDK creates and builds projects at the installed Designer version, so
a v33 project needs a v33 Designer beside it (each a separate install and
activation). The matrix across revisions is therefore an install question
for ECHO1 before it is a harness question; the harness itself is
revision-agnostic (`target.logix.revision`).

### Phase C — check (2026-10-03)

Estimate about a week; one session (the same day as A and B). Timebox not
hit. Built: `naut test --target logix` (§5.5 as the user's own scenarios),
the side-code program with the heartbeat, four conformance projects.

**Stop criteria** — unchanged from Phase B: no Studio GUI anywhere in the
loop; generated rungs online-edit; licence as recorded.

**Re-scope / stop criteria**
- *Special cases:* **0** per-program fixes, **0** per-firmware branches.
  One rule changed from a conformance finding (CTU ends its rung).
- *The equivalent subset is too small to be useful* — the yardstick is
  batch-skid `line/Line.L5X`. Its instruction set is covered (all of
  `XIC XIO OTE OTU TON GE`, plus `LT` once its `LES` caption is fixed),
  and every instruction family now has behavioural equivalence measured on
  Echo. What it cannot express is the UDT (`Line_Status` with a coil on a
  member). **Instructions: not a toy. Types: still v1.** Widening to UDTs
  is Phase D work alongside ST routines.
- *An instruction not equivalent and not rejectable:* **none.** 13 of 13
  conformance scenarios pass on both runtimes, including the places the
  brief named (timer reset while timing, counter at preset, edge at the
  head and not at the head). Not yet probed: compare with NaN, counter
  overflow — both outside anything a v1 program writes.
- *Verification trustworthy:* flake rate **0 of 10** consecutive live runs
  (40 scenario executions, ~5 s per run) on top of 0 of 13 first runs;
  0 Echo/logixd interruptions across every scheduled-style run today
  (about 20 SDK sessions, 7 downloads). Below the 2% and 1-in-5 limits,
  on a small sample.

**DX criteria** — unchanged: ~2 s warm online edit; 2 m 16–23 s per
download; 0 errors first seen at SDK import or build since the time-slice
attribute in Phase B.

**Not done in C:** the version matrix (ECHO1 has one Designer version;
see above), the NaN/overflow probes, and a flake measurement over days
rather than minutes — those want the scheduled run from §5.6, which needs
a runner on the licensed host.

**James's call (2026-10-03): continue to Phase D; stay on v38 and widen the version matrix only if the product gets traction.**

### Phase D — in progress (2026-10-03)

**Step 1, user-defined types** — the one thing the yardstick needed. A
`TYPE X : STRUCT … END_STRUCT; END_TYPE` in a library `.st` becomes a Logix
UDT (nested types first, members in order; BOOL, SINT, INT, DINT, REAL,
LREAL, nested types and one-dimensional arrays; TIME, STRING and block
instances refused by name); a tag of that type is a structure with the
manifest's `init:` map as its seeds; a rung's member path
(`P101.Status.Alarm`, `P101.Hist[2]`) is checked against the type at
check time and passes through verbatim. The L5X reader renders the emitted
UDTs back as the same ST. `naut check --target logix` now passes a
type-only `.st` library, and `naut test --target logix` reads and writes
members by dotted path.

Measured on ECHO1 with the `udt` conformance project (two types, one
nested; a coil on a member, a compare on a numeric member, a contact two
levels down): SDK import and build in 38 s, download verified, **2 of 2
scenarios pass on both runtimes** — including a write to a nested member
over EtherNet/IP that the controller's logic then acts on.

**The yardstick, written:** `logix/writer/testdata/conformance/yardstick`
is batch-skid's `line/Line.L5X` Receive routine as a nautilus project. The
writer emits the export's rung text, with `LES` spelled `LT`
(`TestYardstickMatchesLineL5X`). **Downloaded to Echo and run there: 3 of
3 scenarios pass on both runtimes** (ready below 80 %, the 2 s settle
before accept with a fault dropping it and raising the structure's alarm
member, the high-level cutoff unlatching an accept the same scan the OTE
re-asserts it). The §7a yardstick criterion is closed on both halves:
instructions and types.

**Step 2, Structured Text routines.** `logix/writer/stwriter.go` emits an
ST PROGRAM as a Logix ST routine, statements as written: assignments, IF
/ ELSIF / ELSE, CASE with lists and ranges, FOR / WHILE / REPEAT, EXIT,
the operators, member and index access, ABS / SQRT / LN / LOG / EXP / SIN
/ COS / TAN / TRUNC, `EXPT` as `**`, conversions dropped (Logix converts
on assignment). The dialect gaps as rules: BOOL literals are 1 / 0; TIME
is a DINT of milliseconds everywhere in ST; a timer instance is an
`FBD_TIMER` and `t(IN := x, PT := T#2S, Q => y)` becomes `t.PRE := 2000;
t.TimerEnable := x; t.Reset := NOT (x); TONR(t); y := t.DN;` (the Reset
mirrors IEC's TON, which clears when IN drops; TONR alone holds ACC); a
TOF is `TOFR(t)` without Reset; a counter is an `FBD_COUNTER` driven by
`CUEnable`, `PRE`, `Reset` and `CTUD(c)`. FBD structures are emitted
without initial data and the preset is assigned in code, because their
Decorated layout has not been seen in an export. Refused by name:
STRING, MIN / MAX / LIMIT / SEL / MUX, ATAN2, RETURN, CONTINUE, user
functions and blocks (step 3). `naut check --target logix`, `naut logix
write` and `naut logix deploy` take `.st` programs.

Measured on ECHO1 with the `st` conformance project (CASE, a hysteresis
IF, FOR, a timer and a counter called the IEC way with `Q =>` bindings):
SDK import and build 36.5 s, download verified, **4 of 4 scenarios pass
on both runtimes** — the TONR timer times 2 s and resets when its input
drops, the CTUD counter counts edges, holds done, resets. The facade
notes that the controller refuses a whole-structure read of an
FBD_TIMER (CIP 0x0f) and reads members instead; harmless.

**ST online edits, and a crash that was ours.** An ST routine has no rung
import, so an online edit is a whole-routine import
(`partial-import-with-target`) of a Routine-target partial
(`writer.WriteRoutine`). The first attempt crashed Logix 5000 Services
inside the SDK (`LgxSrv_E_FATAL_ERROR`, offline and online alike): the
import log showed the routine "Overwritten" and then "Renamed" onto its
own name, because the target path was the routine itself. A routine
imports INTO its program's path. With that, the offline import works and
the **online import with FinalizeEdits took 4.0 s on Echo, controller in
Run throughout, routine text verified from the controller.** The SDK
service recovered on its own once the session closed; nothing on the
controller changed. Both deploy paths now use the program path. Recorded
as the rule it is: a container path, never a component's own. From the
CLI, `naut logix deploy --online` of the ST fixture: 2 m 24 s on the cold
path (the SDK opens dominate, as for ladder), verified by routine text.

**Live-test hygiene, found by a rerun:** the ST timer scenario failed on
its second run because the first run's last step had left `In` true on
the controller, and the timer was long done. On nautilus every test
starts from the manifest's seeds; a controller keeps what the last test
left. `naut test --target logix` now writes every input tag's seed before
each test (outputs and state are the logic's, and a scenario that depends
on them establishes them itself). 4 of 4 again, and on reruns.

**Step 3, FUNCTION_BLOCKs as Add-On Instructions.** A ladder or ST
`FUNCTION_BLOCK` from the program's own file or a library becomes an AOI:
`VAR_INPUT` as Required, Visible Input parameters; `VAR_OUTPUT` as Output
parameters (not Required, Visible) read from the instance as `inst.Out`;
`VAR_IN_OUT` as InOut parameters, which Logix only allows for structures
and arrays (a scalar is refused with the rule's name); `VAR` as local
tags; the body as the `Logic` routine through the same lowering as a
program, so a timer, a counter and an edge inside a block get the same
rewrites and generated names. A call `inst:Block(In := x, …, Out => y)`
is the instruction `Block(inst,x,…)` at the head of its rung — every
unbound BOOL input, or `EN`, is the power-in, and a call with a rung
condition in front of it moves to a helper rung so the instruction sits
first — with one copy rung per `=>` binding. `VAR_EXTERNAL` inside a
block is refused (an AOI sees only its parameters). The L5X reader's
`LogicOf` includes AOI definitions, so drift and the deploy diff see a
changed block, and a changed definition forces a download: Logix does
not online-edit an AOI's logic while instances exist.

Two SDK facts cost the day's time, both now carried in code. First,
**a whole-project L5X that declares an AOI instance tag does not
import** (`XMLSrv_E_IMPORT_ABORTED_NO_CHANGES`, and no import log is
written to say why), while the same definition, tags and program
partial-import fine one container at a time. Deploy therefore builds an
AOI project in two steps: the skeleton (controller, types, AOI
definitions, controller tags, side code, the program as a NOP
placeholder) as the whole-project import, then the program — its tags
and routine, controller tags as context — as a Program-target partial
over it. Second, **an instance tag with a `Radix` attribute is silently
dropped** by the importer (a warning, not an error), and the build then
fails with `RxCMP_E_OPERAND_TAGNOTFOUND` on the call that names it.
Four bisections pointed at the definition, the import path and the
operand list before a byte-compare against a hand-written partial that
did build showed the one attribute. A structure tag carries no Radix;
the emitter now knows an AOI instance is one, and a unit test pins the
tag's exact shape.

Measured on ECHO1 with the `aoi` conformance project — the lift
station's `MotorStarter` block (HOA select, 5 s fail-to-run, three-trip
lockout; a TON, a CTU and a rising edge inside the block), instantiated
twice: two-step import and build, **download verified in 2 m 24 s, 3 of
3 scenarios pass on both runtimes** (hand/auto/off, the 5 s fail-to-run
cleared by reset in 12.6 s of wall time, three trips to lockout in
18.8 s). The controller refuses a whole-structure read of an AOI
instance (CIP 0x0f) as it does for FBD timers; the facade reads the
externally visible members and reports the `ExternalAccess="None"`
locals as zero, which is what they are from outside. Not measured: an
online edit of a program that calls an AOI — the rung import path is
unchanged, so nothing suggests it differs, but it has not been timed.

### Phase D — check (2026-10-03)

No estimate was written for D; it took one session (the same day as A,
B and C). Built: UDTs, ST routines with online edit, FUNCTION_BLOCKs as
AOIs with a two-step build; three new conformance projects and the
yardstick.

**Stop criteria** — unchanged: no Studio GUI anywhere in the loop (the
two SDK import quirks above were worked around in the deploy flow, not
by opening Studio); generated rungs and generated ST routines
online-edit; licence as recorded in Phase A.

**Re-scope / stop criteria**
- *Special cases:* still **0** per-program fixes and **0** per-firmware
  branches. Phase D added rules, not cases: TIME-as-DINT and BOOL
  literals in ST, the container-path rule for routine imports, the
  structure-tag shape, the InOut-must-be-a-structure rule.
- *The equivalent subset is too small to be useful:* **closed.** The
  yardstick (`line/Line.L5X`, UDT included) is written in the subset
  and passes 3 of 3 on Echo. The lift station's motor-starter block,
  the largest reusable unit in the examples, passes 3 of 3 on Echo as
  an AOI. Still outside the subset: STRING, unsigned and bit-string
  types, TP/CTD/CTUD, MIN/MAX/LIMIT/SEL/MUX, user FUNCTIONs in ST,
  arrays of block instances.
- *An instruction not equivalent and not rejectable:* **none.** The
  conformance count on Echo is now 13 + 2 (udt) + 3 (yardstick) +
  4 (st) + 3 (aoi) = **25 of 25** on both runtimes.
- *Verification trustworthy:* 0 of 10 flake from Phase C stands; every
  Phase D project passed its first live run and its rerun once the
  reseed landed; the one reseed failure was a harness gap, fixed. **0**
  Echo/logixd interruptions in about 15 further SDK sessions and 6
  downloads today, including one SDK service crash that was our input
  and from which the service recovered unattended.

**DX criteria** — warm online edit 2.1 s (ladder), 4.0 s (ST); full
download 2 m 20–24 s on every project today, now consistently over the
2-minute line by 20–24 s. The time is the SDK's opens, uploads and the
download itself, not the writer; the warm session removes the opens for
edits, and a warm download path is the obvious next cut if the number
matters. *Errors surfacing late:* three in D, all writer defects found on
the SDK and none reachable from a user's program once fixed (routine
import path, AOI whole-import, structure-tag Radix). 0 user-program
errors first seen at import or build; the check target caught every
refused construct in the conformance sources before the SDK saw them.

**Not done in D:** user FUNCTIONs in ST; arrays of block instances;
AOI call online-edit timing; the version matrix and the multi-day flake
number (as in C, they want the scheduled runner); hardware `.L5X`
merge and tag-value preservation across downloads (deferred since B).

### Phase E — brownfield, in progress (2026-10-03)

**James's call after the Phase D check: keep pushing.** The brief's "Later"
phase: existing Logix ladder → editable nautilus source, because every
plant already has the code and "ditch the IDE" has to start from it.

**Built: `naut logix import --project <dir> <file.L5X>`** (`logix/importer`).
The whole export as a nautilus project: `nautilus.yaml` (one task per
imported routine, the scan from the Logix task, `tag-files:`, the
`target: logix` section with the controller's name, processor and
revision, and the comm path / host when given), `tags/logix.yaml`
(controller tags with the export's own descriptions, `role: output` for
every tag the imported logic writes), `lib/logix_types.st` (the UDTs),
one `PROGRAM` per ladder routine (`<Program>.ld`, or
`<Program>_<Routine>.ld` with the program's tags promoted to
`<Program>_<Tag>` controller tags when a program has several routines),
one `FUNCTION_BLOCK` per Add-On Instruction in `lib/`, and ST routines
carried verbatim with their declarations. The L5X reader learned Tasks
and an AOI local tag's `DefaultData`, and exports its identifier
mapping, so the import and the type generator agree on a name.

The import is the writer's mapping run backwards, so the writer's own
idioms fold back to what they came from: `XIC(x)OSR(st,q)` and a later
`XIC(q)` to `+x`; `MOVE(v,t.PRE)` ahead of the timer to `PT := v` (the
variable retyped TIME when the preset is all it feeds); `<cond>RES(c)`
after the count to `R := cond`; the DN-headed continuation after a TOF or
CTU back onto the block's rung; the `en_` helper rung back to the AOI
call's condition; the capture rung `XIC(c.DN)OTE(x)` after a folded
reset to `Q => x`. People's idioms the writer never emits map too: a
one-shot at the head of a leg of the rung's first branch is `+x` (and
the writer now inlines ONS there as well); a one-shot of the whole
condition driving one OTE is the `( P x )` coil — **edge coils joined
the writer's subset** as `<cond>ONS(st)OTE(x)` and `<cond>OSF(st,x)`,
alone on their rung; `CMP(a >= b)` with a plain comparison is the
compare it means; `XIC(t.TT)` is `t.IN /t.Q` (TT is EN AND NOT DN);
`inst.Out` reads of an Add-On Instruction's output are now legal in the
writer, as Logix writes them. A mid-rung coil, a block whose rung-in
passes through, or a branch of output legs becomes one nautilus rung per
output leg sharing the condition — the split the ladder grammar
documents.

**What has no nautilus form is not guessed at.** The rung is left out of
the program, its text kept as a `// not imported (<reason>)` comment
where it stood, the file's header says the program is not deployable
until those are rewritten, and the report counts them by reason. A
routine with a rung left out is never silently "imported".

**Measured on the development corpus** (52 real exports, 30,397 ladder
rungs, 964 ladder routines of which 831 are AOI logic; the corpus stays
out of the repo, `NAUTILUS_L5X_CORPUS=… go test ./logix/importer/ -run
Corpus -v` reruns it):

- **55.9 % of rungs import** (16,984 of 30,397); **13.3 % of routines
  import complete** (128 of 964; program routines 15 of 133, AOI logic
  113 of 831).
- **Every complete program routine writes back identical**: 15 of 15
  re-emit the export's rung text, modulo the generated edge tag names.
  The writer refused none of them. Same for the writer's own output: the
  DemoLine export, the whole v1 subset golden, the UDT and the AOI
  conformance projects import and write back identical.
- **What keeps the rest out, by rung:** data operations 10,216 (MOVE
  6,008, MUL 1,806, SUB 940, ADD 501, CPT 339, LIMIT 322, DIV 300) —
  **34 % of all rungs**; bit-level access `Tag.3` 891; `CMP` with an
  expression beyond a binary comparison 733; a one-shot inside a branch
  leg that also carries coils 440 + compound one-shots 353; arrays of
  timers 153; module I/O operands 151; GSV/SSV 153; a timer inside a
  branch leg 100; OSR away from the helper shape 100; JSR 14.
- **The Studio-authored DemoLine export** (the real one, from the Echo
  share) imports complete, `naut check --target logix` passes, and the
  writer re-emits its two rungs byte for byte. Deploying that import
  back to Echo was not run: the session's permission classifier refused
  the deploy from the scratch directory, and I did not try another
  route. The command is one line for James.

**The finding that needs a decision.** Real Logix ladder is a third data
operations — a MOVE, an ADD, a CPT on a rung — and nautilus ladder has
no element for any of them: a `.ld` rung's elements are contacts, edges,
compares, blocks and coils, and the one way to store a number is a
block's `=>` binding. That is why 87 % of routines import incomplete,
and it is a language question, not an importer one. The options as I
see them:

1. **Add data instructions to nautilus ladder** — an output-zone element
   that assigns on rung condition (`[ y := x ]`, or Logix-style
   `MOVE(x, y)` / `ADD(a, b, y)` boxes), compiled through the same FBD
   lowering as everything else. The writer emits MOVE/ADD/SUB/MUL/DIV/CPT
   from it; the import maps them back one for one. This closes most of
   the 34 % and also lets a greenfield Logix-style author write the
   rungs they already write. It is a nautilus language change: lang/ld,
   the editor's palette and renderer, the docs.
2. **Import such rungs as ST** — a routine with data operations becomes
   an ST PROGRAM (`IF a THEN y := x; END_IF;`), losing the ladder view
   for that routine but importing it whole and deployable (the writer's
   ST routine). Mechanical, no language change, worse to read.
3. **Leave them out** and sell brownfield as "boolean logic imports,
   data moves are rewritten by hand" — honest, and 55 % is not nothing,
   but it is not "the plant's code is now nautilus source".

I would take option 1, and do option 2 as the fallback for routines that
still do not fit. Bit-level access (`Tag.3`, 3 % of rungs) is a second,
smaller language question of the same kind.

**Not done in E:** the controller leg above; the data-operation decision;
RTO/CTD; `AFI`; multi-dimensional arrays; aliases (declared as tags,
their I/O target not carried); JSR (each routine is its own PROGRAM; the
call rung is left out); the `( P x )` coil in the round-trip comparator
beyond the writer's unit tests.

### Phase E — data primitives (2026-10-03, later the same day)

**James's call on the data-operation finding:** "if we're going to do this
we're going to need to have primitives that are well used in Logix... like
MOVE... so let's add things", and no ST fallback without understanding
what does not fit. Also: deploy to the Echo emulator at will.

**Built: the ladder assignment.** `{ y := expr }` is a new nautilus ladder
element (`lang/ld`): made when the rung has power at that point, power
passing through unchanged — the IEC function box with `EN` on the rung,
written as the assignment it is. Several with `;`. The value is any IEC
expression; `lang/ld` now has an expression parser (`ParseExpr`,
precedence per the standard) and spells it for the FBD netlist as prefix
calls, lowered as `y := SEL(cond, y, value)`. Function contacts take
expression arguments the same way (`GT(Raw / Span * 100.0, 50.0)`), which
they could be written with before but did not compile. The diagram
renders and edits the element (palette `{ := }`, dblclick to edit); the
docs (`docs/functions.md`, the package doc) say what it is and the one
semantic note: the value is evaluated every scan like any block, so an
index in it must stay valid while the rung is false, where a Logix box is
skipped.

**The writer** maps an assignment to the instruction its shape names —
`MOVE`, `ADD`, `SUB`, `MUL`, `DIV`, `ABS` — and anything else to a `CPT`
in Logix's expression spelling (`SQRT`→`SQR`, `TRUNC`→`TRN`, `EXPT`→`**`,
TIME literals as milliseconds); a compare with an expression operand is a
`CMP`. `MOD`, `NEG`, `SQR`, `XPY` exist as instructions too, but no export
in the corpus carries them, so those shapes go through `CPT`, which the
corpus does (the mnemonic allowlist rule from Phase A). Refused by rule
(`logix/data-op`): an assignment of a comparison or boolean (drive the
BOOL with a coil), and functions Logix cannot spell (MIN, MAX, LIMIT, SEL,
MUX, strings, shifts). The round-trip comparator checks both.

**The importer** maps `MOVE ADD SUB MUL DIV MOD NEG ABS SQR XPY CLR CPT`
to assignments, `CMP` to the compare it means (sides as IEC expressions),
`LIMIT` with literal bounds to the two compares it is (a variable bound
wraps in Logix when low > high — refused, 4 rungs). And the 2-D habits of
real Logix ladder come across as the rungs they are: an output leg inside
a branch is a rung of its own and the branch keeps its conditions; a timer
or counter inside a leg (it passes its rung-in on) is hoisted onto its own
rung; parallel boxes stay a branch of boxes; a one-shot of a compound
condition driving one OTE is the `( P x )` coil, driving anything else
gets a pulse tag of its own (`cond ( P os_r7_1 )`, then `os_r7_1 rest`);
`OSR`/`OSF` mid-rung are `( P q )` / `( N q )` output legs; conditions
left dangling after the last output (which drive nothing in Logix either)
are dropped. Every refusal now carries its reason into the file and the
report.

**Measured on the corpus** (same 52 exports):

| | before (Phase E start) | now |
|---|---|---|
| rungs imported | 55.9 % | **93.9 %** (28,541 of 30,397) |
| routines complete | 13.3 % | **66.1 %** (637 of 964) |
| AOI Logic routines complete | 13.6 % | **73.5 %** |
| complete program routines written back | 15 identical of 15 | **17 identical + 9 restructured of 26**, 0 refused by the writer |

"Restructured" is the compound one-shot: one Logix rung becomes two
nautilus rungs with a pulse tag, equivalent and not byte-identical; every
other complete routine re-emits the export's rung text.

**Measured on Echo:** the `data` conformance project (an ADD per edge, a
CPT scaling and an ABS, MUL/SUB, a CMP, boxes in parallel legs, a
compound one-shot, MOVEs of literals) — download verified in 2 m 19 s,
**4 of 4 scenarios pass on both runtimes.** Two things the live run
taught, both harness, not semantics: REAL arithmetic on the controller is
32-bit, so `60.0 / 100.0 * 100.0` lands a few ULPs off and the scenario
uses `{near: 60.0}`; and a one-scan pulse is invisible to a 100 ms poll,
so the scenario latches it. **The brownfield DemoLine** — the real
Studio-authored export, imported — deployed to Echo and verified in
2 m 23 s: the plant's export is nautilus source and is back on the
controller.

**What still does not fit, and why** (6.1 % of rungs):

- **Bit-level access `Tag.3`** — 917 rungs, 3.0 %. A contact on bit 3 of
  a DINT, a coil on bit 12 of a status word. nautilus has no bit-of-
  integer addressing: integers are values, BOOLs are tags. A read could
  be spelled `NE(Word AND 8, 0)`; a coil cannot be spelled at all without
  a read-modify-write. This is the second language question, same shape
  as the first: Logix programmers pack BOOLs into words everywhere, and
  nautilus would need `Word.3` as an addressable BOOL in all three
  languages (and in the tag store, where the live overlay reads it).
- **Arrays of timers and counters** — 179 rungs, 0.6 %. `TON(T[3],?,?)`.
  nautilus declares block instances one by one; an array of instances is
  a language gap (the writer refuses it in the forward direction too).
- **Module I/O operands** — 161, 0.5 %. `Local:1:I.Data.3` read straight
  off the rack. A brownfield site aliases these to tags before import,
  or the import gets an I/O map; nautilus has no rack addressing.
- **Opaque types** — 159, 0.5 %. MESSAGE tags (the MSG instruction's
  control block) and tags whose export carries no type.
- **RES on a timer** — 116, 0.4 %. `XIC(t.DN)RES(t)` restarts a running
  timer: an IEC TON has no reset input, it restarts when IN drops. The
  self-resetting-timer idiom could become a block of its own; not done.
- **GSV / SSV** — 153, 0.5 %. Controller system values (wall clock,
  fault codes, task scan times). No nautilus equivalent; a side-code
  question.
- **One-shots inside a leg that is not rail-fed and carries coils** — 54.
  The pulse-tag rewrite handles the leg's conditions; a leg that also
  has its own coils needs the output-leg split and the pulse at once.
  Doable, not done.
- **JSR** (30), `COP`/`BTD` (37), `TND` (12), MSG (5), string ops (2):
  subroutine calls (each routine is its own PROGRAM, the call rung is
  left out — a `JSR` with parameters has no nautilus form), block copies
  and bit distributes (no element), temporary end, messaging.

**Not done in E:** bit access and instance arrays (decisions); the
one-shot-with-coils leg; the alias/I-O map; RTO/CTD; `( P x )` in the
round-trip comparator is checked by shape only.

### Phase E — bit access and arrays of instances (2026-10-03, night)

**James's calls:** bit access — "super helpful and common in PLC IDEs so
I'm for it"; arrays of instances — "pretty for that too, it's
convenient"; module I/O — "we'll need to figure out how to handle I/O";
the long tail — tempted by a nautilus setting "I'm using an A-B PLC as a
runtime" that enables Allen-Bradley-like instructions within reason,
since refusing them "seems a little hostile to users".

**Built: bit access.** `Word.3` is bit 3 of an integer as a BOOL, in all
three languages: a `BitRef` lvalue in the IR, read as a shift-and-mask and
written as a read-modify-write of the word; the ST parser takes a number
after the dot, the FBD netlist and the ladder reference grammar likewise;
the live overlay reads the bit out of the streamed word. Found on the way:
the type checker accepted `INT AND INT` as a bitwise integer while the VM
evaluated AND and OR as booleans only — fixed, the VM is bitwise on
integers now. The writer passes `Word.3` through (it is Logix's own
spelling, on scalars, array elements and UDT members alike); the importer
no longer refuses it (917 corpus rungs).

**Built: arrays of instances.** `Timers : ARRAY [0..3] OF TON;` — each
element its own instance with its own retained state, called by index in
ST (`Timers[2](IN := …)`) and in a rung (`Timers[2]:TON(PT := …)`), read
by index; the index may be a variable. The IR's call carries an instance
lvalue; frame migration already carried arrays recursively. The writer
emits one `TIMER[4]` / `COUNTER[2]` tag with per-element presets in its
data; the reader now decodes arrays of structures. The importer declares
the array once and maps `TON(T[3],?,?)` to `T[3]:TON(PT := …)` with the
element's preset (179 corpus rungs).

**Two findings from Echo, both now rules or code:**

- **A Logix MOVE takes no BOOL.** `{ Cmd.7 := Status.1 }` built to
  `RxCMP_E_AUDIT_INVALIDOPTYPE`. The writer refuses an assignment to a
  BOOL or a bit at check time and names the coil to use (`( S x )` /
  `( R x )`); the bits conformance project writes the bit copy as two
  set/reset rungs.
- **A MOVE to `.PRE` is sticky; a nautilus call binds PT every time.** The
  arrays project presets `Timers[0]` by literal and `Timers[Sel]` by a
  MOVE. One scan with `Sel = 0` (the harness reseeding inputs) overwrote
  `Timers[0].PRE` for good on the controller; on nautilus the next call
  bound PT back. Exact rule: when any call indexes an array of blocks
  with a variable, every call on that array presets through a MOVE ahead
  of its rung. The importer folds those MOVEs back into `PT :=`.

**Measured on Echo:** `bits` 4 of 4 (contacts and coils on bits, a bit
latched into another word, a masked compare), `arrays` 2 of 2 (four
timers in one array, literal and computed index, a counter element
counting a timer element's edges). Both pass on nautilus too. Downloads
1 m 50 s – 2 m 24 s.

**Measured on the corpus** (same 52 exports):

| | start of Phase E | after data primitives | now |
|---|---|---|---|
| rungs imported | 55.9 % | 93.9 % | **97.0 %** (29,472 of 30,397) |
| routines complete | 13.3 % | 66.1 % | **70.1 %** (676 of 964) |
| complete program routines written back | 15 of 15 | 17 + 9 of 26 | **53 identical + 12 equivalent of 65**, 0 refused |

**What still does not fit (3.0 % of rungs), and why:**

- **Opaque types** — 187. MESSAGE control blocks (the MSG instruction's
  tag) and alias tags whose export carries no type. Comes with the I/O
  story below and with the dialect question.
- **RES on a timer** — 179. `XIC(t.DN)RES(t)` restarts a running timer,
  free-running pulse idiom. An IEC TON has no reset input; it restarts
  when IN drops. Candidate for the dialect setting: a `TONR`-style block
  with a Reset pin that the writer emits as TON + RES and the importer
  folds the RES into.
- **Module I/O operands** — 161. `Local:1:I.Data.3`. Proposal below.
- **GSV / SSV** — 153. Controller system values. Dialect candidate.
- **One-shots inside a leg that also carries coils** — 54; **mixed arrays
  of blocks** (one array driven by TON in one rung and TOF in another) —
  54; JSR with parameters 30; COP 35, MSG 21, TND 12, BTD, string ops.

**Proposal: I/O as alias tags.** Logix already has the mechanism: an
alias tag (`TagType="Alias" AliasFor="Local:1:I.Data.3"`) is how a Logix
program names a rack point. A nautilus manifest tag would carry the
binding — `- { name: StartPB, role: input, alias: "Local:1:I.Data.3" }`
— the writer would emit the tag as an alias, and the importer would turn
an export's alias tags into manifest tags with `alias:` and rewrite
direct `Local:…` operands into generated alias tags. nautilus source
never sees a rack address; the manifest is the one place the hardware
binding lives; the nautilus runtime treats the tag as any other. What it
needs that we do not have: the module configuration itself (the rack) in
the project, which is the hardware `.L5X` merge deferred since Phase B,
and which Echo cannot exercise without a module. Not built; one question
is whether the binding belongs in the manifest or in a sibling `io.yaml`.

**Proposal: the dialect setting.** `target: logix:` is already the place
a project says it runs on Logix. A `dialect: logix` (or the target's
presence) could enable a library of blocks with Logix semantics that
nautilus implements natively — `TONR` (timer with a Reset pin), a
free-running pulse block, `GSV`-style reads of a small set of system
values (wall clock, scan time), `COP` for arrays and structures — each
written as a FUNCTION_BLOCK in a nautilus library so the runtime, the
writer and the importer all have one definition. "Within reason" is the
list; MSG, JSR with parameters and string manipulation stay out.
Not built; needs James's list.

### Phase E — dialects and I/O aliases (2026-10-04, early)

**James's call:** both proposals approved, the dialect generalized —
`dialect: logix | siemens | codesys`, the default being the nautilus
runtime itself.

**Built: `dialect:`.** A manifest key (`internal/dialect`): a dialect is a
library of blocks with the vendor's semantics, written in nautilus ST and
embedded in the binary, joined to every program of the project ahead of
its own libraries by both composition paths (the runtime's `Libraries`,
the editor's and checker's `PreludeSources`). One definition, three uses:
the nautilus runtime runs the block, the Logix writer emits the native
instruction for it, the L5X importer folds the native idiom back into
it. `nautilus` adds nothing; `siemens` and `codesys` are accepted and
empty. The first `logix` block is **`TONR`** — a TON with a `Reset` pin,
the `RES(t)` idiom: the writer emits `<Reset>RES(t)` ahead of the timer's
rung (ladder) or `t.Reset := NOT(IN) OR (Reset)` with `TONR(t)` (ST); the
importer folds `XIC(x)RES(t)` on a TON-run timer into `t:TONR(Reset :=
x)` and sets `dialect: logix` in the manifest it writes.

**Built: tag aliases.** A manifest tag may carry `alias:` — the Logix
alias tag's target, how a Logix program names a rack point or another
tag. The writer emits the tag as `TagType="Alias" AliasFor="…"`; the
importer turns an export's alias tags, and rack points the logic names
directly (`Local:1:I.Data.3`), into tags with `alias:` (a trailing bit
index types the guess BOOL, else DINT; the module gives the real type on
the controller). The nautilus runtime ignores the binding. Controller
status flags (`S:FS`, first scan) are not rack points: refused by name,
113 rungs — a first-scan block is the obvious next dialect entry.

**Measured on Echo:** the `dialect` conformance project (a free-running
TONR pulse counted by edge; a TONR an input resets while it runs) — 2 of
2 on both runtimes, download 1 m 45 s. Aliases cannot be exercised on
Echo without a module in the project (the hardware `.L5X` merge deferred
since Phase B); they are tested through the writer and the reader.

**Corpus:** 97.2 % of rungs (29,537 of 30,397), 70.4 % of routines; 53
identical + 15 equivalent write-backs of 68 complete program routines, 0
refused. The "io" bucket (161) is gone; "RES" fell to 162 (timers run by
TOF or RTO, or reset in a routine that does not run them); "status" is
113. The equivalent-not-identical count rose with TONR: the writer puts
the RES rung directly ahead of the timer's rung, where the export may have
had it elsewhere — semantically the block's definition (reset dominates
at the call), textually a move.

**Open:** the dialect list beyond TONR (first scan, `GSV` wall clock and
scan time, `COP`); the hardware configuration for aliases to land on;
whether the alias binding belongs in the manifest or a sibling I/O file.

### Pre-PR (2026-10-05)

Rebased onto main, website guide *Authoring for Logix (experimental)*,
experimental labels on the target's CLI surface, extension CHANGELOG;
draft PR #225. Known gap carried as a follow-up: `naut lsp` does not run
the Logix target's rules, so the editor's live diagnostics do not show
them yet (§5.1); `naut check` does.

**James's call (2026-10-05):** guide and this phase log reviewed and
approved; the PR comes out of draft.

### Follow-up — first scan and the wall clock (2026-10-05)

**James's calls:** dialect blocks before I/O modules, in corpus order;
and nothing Logix-specific in the language ("as standard as possible"),
so the two corpus idioms with a standard equivalent became **core**
features, not `dialect: logix` blocks.

**Built: `FIRST_SCAN()`.** A BOOL function, TRUE for the whole of a
program's first scan after a start or download; an online edit (a warm
swap, state kept) and a rollback are not a start, as `S:FS` is not set
by a Logix online edit. Builtin functions gained an optional host-aware
form (`ir.HostFn`) and the host an optional `ir.ScanInfo`, which the
runtime's scan view answers from the program's cold-start flag. Ladder
uses it as a function contact (`FIRST_SCAN()`, `/FIRST_SCAN()`), ST as
an expression. The writer emits `XIC(S:FS)` / `XIO(S:FS)` and `S:FS` in
ST; the importer turns an `S:FS` contact back. Other status flags
(`S:N`, `S:Z`, …) stay refused by name.

**Built: `LOCAL_TIME`.** A block with no inputs and the outputs of IEC
`SPLIT_DT` (`YEAR`, `MONTH`, `DAY`, `HOUR`, `MINUTE`, `SECOND`,
`MILLISECOND`), read from the runtime's clock through an optional
`ir.WallClock`: local time in production, the virtual clock in UTC under
`naut test`, so calendar tests are machine-independent. In a rung it has
no power pins (it sits on the rail, power passes through), which is the
only shape GSV has in the corpus. The writer makes an instance a `DINT[7]`
tag, a call `GSV(WallClockTime,,LocalDateTime,clk[0])`, a member read its
element; `MILLISECOND` (Logix keeps microseconds) is an ST expression and
refused in ladder. The importer turns `GSV(…,arr[k])` into the POU's
`wallClock` instance plus one assignment copying the seven outputs into
`arr[k..k+6]` (microseconds as `MILLISECOND * 1000`); the writer folds
that pair back into the one GSV, and drops the instance's tag, when
nothing else reads the instance, and otherwise fills the instance and
copies element by element. GSV of anything else (`Task`, `LastScanTime`:
none in the corpus) stays refused.

**Not built: `COP`.** Classified by operand type, the corpus's COPs are
byte serialization: DINT and INT into SINT byte buffers, socket
structures into bytes and back, for `MSG`/socket payloads. Five are
same-type array copies. Emulating them needs a byte-layout memory model
nautilus deliberately lacks, and the routines are blocked by `MSG`
anyway. **James's call:** skip it; the remaining gap is messaging.

**Corpus:** rungs 97.2 % → 97.9 % (29,752 of 30,397); ladder routines
complete 70.4 % → 81.0 %; AOI routines 73.5 % → 85.8 %; write-backs
unchanged (53 identical + 15 equivalent of 68, 0 refused). The "status"
and "GSV" buckets are gone; the largest left are MESSAGE types (187),
`RES` (162), `SSV` (51), `COP` (35).

**Measured:** conformance project `system` (the corpus idiom: GSV into an
array, a sequencer seeded on the first scan, a once-a-second tick, plus a
clock read by member): 3 of 3 on nautilus and 3 of 3 on Echo; the SDK
built the GSV and `S:FS` unchanged (download 2 m 13 s). One controller
fact found on the way: a Logix controller whose clock was never set
reads 1998-01-01 (Echo after its service restarts), so the scenario
asks for a calendar, not the date. Echo itself needed attention first:
after the VM rebooted, Tailscale wanted a desktop login (James), and the
Echo service needed a restart to bind its controller to the address
again, which emptied the controller.

## 8. The demo this enables

James's target demo (2026-10-03), which replaces the Tier A `ab01` draft in the
content repo:

1. Create a program in nautilus (VS Code, ladder).
2. Download it to the Allen-Bradley controller (Echo).
3. Open Studio 5000 and see the same program, as proof only.
4. Change the logic in nautilus; push it as an **online edit**, or download.
5. Live values from the running controller on screen the whole time.

Fixture: the DemoLine logic (pump seal-in, high-level alarm), written as
nautilus LD, on Echo, with files on echo-vm's **Y:** share. Never show echo-vm's
Z: (client work).

## 9. Handoff for the build session

- Branch `logix-authoring`, worktree
  `~/Development/joyautomation/nautilus-logix-authoring`, cut from `main` at
  `d3a4bdb` (PRs #15/#16 merged).
- Read first: this brief; `logix-target.md` §4, §6.4, §15.2, §20, §21, §22;
  `lang/ld/ld.go` (rung grammar); `lang/l5x/rll.go` (neutral text);
  `tools/logixd/Program.cs` endpoints.
- Start with Phase A. Nothing in it needs Echo or Windows.
- This is a proof of concept with kill criteria (§7a). Check the SDK licence
  terms during Phase A, and at the end of each phase measure every criterion
  and log it in §7b before asking James whether to continue.
- Echo: `ssh echo1` (10.154.92.210; CIP on :44818 when Tailscale refuses).
  Fast Startup is off; restart with `incus restart echo-vm --timeout 300`.
  Don't change the controller's mode without asking James.
