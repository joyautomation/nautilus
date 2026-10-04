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
runtime. Open: `scans: n` on a controller is time-based (there is no scan
count to read); a writer-emitted heartbeat rung would make it exact. The
per-instruction conformance projects and the version matrix are next.

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
