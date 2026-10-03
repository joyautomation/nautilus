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

*(empty)*

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
