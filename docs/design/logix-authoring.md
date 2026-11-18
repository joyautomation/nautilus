# Design brief: author Allen-Bradley logic in nautilus, with Studio 5000 as middleware

Status: **direction set, nothing built.** Written 2026-10-03 on branch
`logix-authoring` (worktree `~/Development/joyautomation/nautilus-logix-authoring`).
This brief **supersedes the recommendation in [`logix-target.md`](logix-target.md)
§8** for the Allen-Bradley development use case. Everything else in that
document still stands, and most of what it built is reused here.

The current caveats, the list the video scripts quote, are in
[§10](#10-caveats-the-videos-quote).

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
| `TIME` | DINT milliseconds | a variable only where it feeds a timer preset; a timer's `t.ET`/`t.PT` as a number is `TIME_TO_DINT(t.ET)` ↔ `t.ACC`/`t.PRE` in a box or a CPT, and `{ t.ET := DINT_TO_TIME(v) }` ↔ `MOV(v,t.ACC)` for a timer no block runs (#793); a write to a timer a TON/TOF runs is refused on import |
| global (`VAR_EXTERNAL`) | controller tag | |
| a manifest tag a program names undeclared (#177/#210) | controller tag, typed from the manifest | ladder and ST (#248); uses resolved on the compiler's AST (`logix/writer/uses.go`) |
| enumeration type and its members | `DINT`; a member is its value | Logix has no enumerated type; the names do not reach the controller, and an import brings the tag back as `DINT` (`logix/writer/enum.go`) |
| `X_TO_Y` / `TO_Y` / `TRUNC_<int>` in ST | Logix that computes nautilus's result, or `logix/conversion` | narrowing as an integer-arithmetic wrap, int → REAL as `v` or `(v * 1.0)`, BOOL → number as an `IF`, REAL → integer as a plain store of the whole right-hand side into a tag of that type; REAL → integer inside an expression, an integer `/` whose quotient meets a REAL (#361; on a rung too, #368), unsigned, bit strings, LINT, LTIME and STRING refused (`logix/writer/convert.go`, #255) |
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
   **Decided and built (2026-10-14, #275):** the merge, as proposed. A
   project names an L5X with `target.logix.hardware` (the import writes
   `hardware.L5X` at the project root); the writer copies its `<Modules>`
   verbatim in place of the generated `Local` module, and the alias tags'
   modules are checked at write time (`logix/io-module`). Controller model
   and revision stay in `target.logix`, checked against the file's `Local`
   module (`logix/hardware`). Generated stubs from a manifest description
   are not built; the stub evidence is in §7b, "Follow-up — the hardware
   configuration".
2. **Online edit or download.** Rung-only changes go as an online edit
   (verified path). New tags or structural changes need a download, which
   stops the controller and so needs explicit confirmation. Unverified: how
   much the SDK allows online beyond rungs (tags added online, for example).
3. **Project layout.** A nautilus project gains a `target: logix` (controller,
   comm path, firmware revision) so `naut check`, the editor's Download
   button and the deploy command all know what they are aiming at.
4. **Tag-value preservation across downloads.** Open since `logix-target.md`
   §15.2. Needed before anyone downloads to a plant. **Decided and built
   (2026-10-14, #273):** a download keeps the controller's values of the
   tags that survive it unchanged, taken from the upload that precedes the
   build; `--reset-values` opts out. The rule and the Echo results are in
   §7b, "Follow-up — a download keeps the controller's values".
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
  worth a one-token fix in `examples/` (made 2026-10-14, #372). The real gap is the data model:
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
`AB_ETH-1\192.0.2.21\Backplane\0`, found by probing slots over
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
  `XIC XIO OTE OTU TON GE`, plus `LT` once its `LES` caption is fixed —
  fixed 2026-10-14, #372),
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
writer emits the export's rung text byte for byte
(`TestYardstickMatchesLineL5X`; until #372 corrected the example's `LES`
caption, with `LES` spelled `LT`). **Downloaded to Echo and run there: 3 of
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
  of its rung. The importer folds those MOVEs back into `PT :=`. A
  literal-index element keeps its preset in the tag's data as well (#331):
  that is its value from download until its first call.

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
the controller). An export's alias tag carries no DataType, so a rung
naming one is typed through the target (#330): a tag, a UDT or Add-On
Instruction member, an array element, a bit, or a rack point's bit (a
BOOL whatever the module). A whole rack word is typed only by the module
definition the export does not carry, so an alias of one is refused by
name rather than guessed, and so are an alias of a whole TIMER, COUNTER
or AOI instance (a nautilus block instance is never a binding) and a
program-scoped alias tag (`alias:` is a controller tag's). The nautilus
runtime ignores the binding. Controller status flags (`S:FS`, first
scan) are not rack points: refused by name, 113 rungs — a first-scan block is the obvious next dialect entry.

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
them yet (§5.1); `naut check` does. (Closed by the follow-up below.)

**James's call (2026-10-05):** guide and this phase log reviewed and
approved; the PR comes out of draft.

### Implicit tags in every program (2026-10-06, #248)

Manifest tags are in scope in every program without `VAR_EXTERNAL`
(#177/#210). #243 gave a ladder program's undeclared tags controller tags
by scanning rung text for identifiers; an ST program got none, so its L5X
named tags the project did not create. The writer now collects uses on the
compiler's AST for both languages — the ST parse, and for ladder its
transpilation through FBD to ST, the source the runtime compiles — resolving
each name the lowerer's way: the program's declarations first, then the
manifest's tags, case-insensitively (`lang/ir/names.go`). Member names, pin
names, function names and `Type#Member` literals are not uses, which also
fixes the ladder scan's false hits (`P.Speed` made a `Speed` tag, `T#2S` an
`S`). References in an ST routine take the declared spelling. FBD and SFC
programs are not in the target, so nothing changes for them.

Enumerations (#238) had no Logix mapping: they are `DINT` now, members
folded to their values in ST and in rung operands, `TO_INT` / `TO_<Enum>`
passed through like `X_TO_Y`.

**Measured on Echo:** conformance project `implicit-st` (an ST program
declaring one tag of eleven it names: a UDT tag, an enumeration tag, a tag
that is only a member name, one named nowhere) — SDK import and build 35 s,
download verified by upload; the controller's tag list (CIP browse) is
exactly the ten tags named plus the side code's `Nautilus_Scan`, `State` a
`DINT` and `Pump` a `PumpData`; the task scans and the values read back
match the program (`Count` 6, `StateNo` 1, `HiSP` 80 from the manifest).
4/4 scenarios on nautilus.

### Follow-up — the target's rules in the editor (2026-10-05)

**Built:** `naut lsp` runs the Logix target's rules on every keystroke,
closing the §5.1 gap the pre-PR entry carried. It matches `naut check`:
only in a project whose manifest has `target: logix` (read through the
LSP's modtime-keyed manifest cache, so adding the section takes effect
without a restart), only on a buffer that compiles, with the same library
sources (the dialect's first, unsaved sibling buffers winning). Each rule
is an error squiggle over its whole line (the writer reports no column),
source `nautilus (logix target)`, the rule ID as the diagnostic's code.
One behaviour was settled for both: a file with no PROGRAM, in any
language, is a library and is checked where a program uses it, so an FBD
block library no longer draws the "only ladder and ST" diagnostic from
`naut check`; an FBD or SFC *program* still does, on its PROGRAM line.
**Cost, measured on the corpus** (133 imported program routines, writer
check only, steady state): it first ran 9 ms median and up to ~150 ms on
a 45-line routine with a large project library, because every operand's
block lookup rescanned every library. The writer now indexes the
libraries once per lowering: 4.3 ms median, 13 ms p90, 26 ms worst, 7 ms
for the largest (1,385-line) routine; no debounce. Corpus numbers
unchanged (97.2 % of rungs; 53 identical + 15 equivalent, 0 refused).

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

**Revisited 2026-11-15: like-for-like COP and CPS.** An instructor's
student project used CPS, which the corpus never does (0 of 209 copies).
**James's calls:** import COP/CPS when they copy like for like; byte
packing between elementary types is a later, separate step; full Logix
UDT layout is declined (every UDT-to-bytes COP in the corpus sits in an
export that also messages over sockets, which stays refused); `MSG` comes
next as IEC 61131-5-shaped `READ`/`WRITE` blocks on the existing CIP
client, CIP Generic (socket) messages refused.

The shape (logix/importer/copy.go, logix/writer/copy.go): a whole array
or structure is `{ B := A }`, which the writer emits as `COP(A,B,n)`;
Logix assigns neither, and the writer used to emit `MOVE(A,B)` (LD) and
`B := A;` (ST) for it, both invalid. A slice is spelled element by
element with `(* @logix COP n *)` on its last assignment (n, the run's
length, left out for 1), and any CPS carries `(* @logix CPS n *)`
(James's call, over "always COP" and "always CPS"); the writer folds
exactly the n marked assignments back into the one instruction, checks
they still step through the same two arrays, and never folds unmarked
code. A comment inside an assignment box is new for
this: it is ignored by the compiler and kept on the assignment. On
nautilus every copy already has CPS's guarantee: a scan snapshots its
tags in and commits them as one unit (runtime/program.go). Refused, each
by reason: a copy between types, a length or index that is not a
constant, a length past either end, and source and destination
overlapping in one array (the result depends on Logix's copy direction,
which is not pinned on a controller).

**Corpus:** COP refusals 35 → 26 rungs (16 byte reinterpretation, 4
ring-buffer slices at a variable index, the rest other reasons); rungs
29,791 → 29,792 (most freed rungs stop next on `MSG`/`MESSAGE`);
write-backs unchanged (68 identical, 19 differ, 0 refused).

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

### Follow-up — conversions written exactly or refused (2026-10-13, #255)

Phase D dropped every conversion ("Logix converts on assignment"), and
that ran differently: `DINT_TO_INT(x)` of 70000 stayed 70000 (nautilus
4464), `REAL_TO_DINT(r) + 1` was not rounded inside the expression,
`DINT_TO_REAL(a) / 2` divided as integers, `DWORD_TO_DINT(REAL_TO_DWORD(r))`
moved the number instead of the bits. Logix has no conversion functions;
it converts as it stores a value or mixes operand types. So the ST writer
(`logix/writer/convert.go`) now types each argument from the
declarations and writes each conversion as Logix that computes the same,
or refuses it with **`logix/conversion`**:

| Conversion | Written as | Why it is exact |
|---|---|---|
| integer → narrower integer or TIME | `((((v MOD 65536) + 98304) MOD 65536) - 32768)` (INT), `… 256 … 384 … 128` (SINT); `v` when it already fits; a literal folded | the first `MOD` leaves a remainder of either sign, the offset makes it positive, so the result is the low bits read signed whichever sign rule `MOD` has; integer arithmetic only, no `AND`/`XOR` |
| integer → wider integer, TIME ↔ DINT | `v` | a tag of that width holds only values that fit; Logix integers are 32 bits |
| the argument wider than the source (`INT_TO_DINT(dint)`) | the wrap to the source width | nautilus reads the argument as the source type first |
| integer → BOOL, REAL → BOOL | `(v <> 0)`, `((v MOD 65536) <> 0)`, `(v <> 0.0)` | nonzero, at the source width |
| integer → REAL | `v` as the whole right-hand side or beside a REAL tag or literal, else `(v * 1.0)`; a tag or literal only | Logix converts the operand itself in each of those places; an integer *expression* is refused, since Logix may evaluate it in REAL once it meets one |
| integer → LREAL, REAL ↔ LREAL | `v`, as the whole right-hand side only | the store converts; mixing inside an expression is unverified |
| BOOL → number | `IF b THEN x := 1; ELSE x := 0; END_IF;`, whole right-hand side only | no arithmetic on a BOOL |
| `TRUNC_<int>(r)` | `TRUNC(r)`, as the whole right-hand side of an integer assignment | the value stored is already whole, so no rounding rule applies |
| REAL → integer or TIME | `r`, as the whole right-hand side of an assignment to a tag of the conversion's own type (`i := REAL_TO_INT(r)` → `i := r;`, `t := REAL_TO_TIME(r)` → `t := r;`) | the store rounds halves to even, as nautilus does (verified on Echo, below); TIME is a DINT of milliseconds, the unit `REAL_TO_TIME` counts. Refused inside an expression (Logix would compute with the unrounded REAL), into another type (the store rounds to the destination's width) |
| LREAL → integer or TIME | refused | not verified on a controller |
| unsigned, BYTE…LWORD, LINT, LTIME, STRING | refused, the type named | no Logix type in the subset (the importer reads LINT and the unsigned types in brownfield declarations; the writer declares none) |

A narrowing wrap is also refused inside an expression that mixes in a
REAL (Logix might compute its `MOD`s in REAL); a comparison's operands and
an array index count as computations of their own. Two divergences stay,
as they do for every value: a REAL is 32 bits on the controller and 64 in
nautilus, and a REAL with no value in the target faults the nautilus scan
where the controller rounds it and stores the result's low bits (below).
In ladder (#777), `{ y := X_TO_Y(v) }` with the conversion as the whole
right-hand side, into a tag of type Y, is the box's own store: REAL → SINT/
INT/DINT (the store rounds halves to even, verified below), DINT/INT →
INT/SINT and a timer's TIME → SINT/INT/DINT (the store keeps the low
bits: Logix's documented integer conversion, not yet run on a
controller). The box is written as it would be for `{ y := v }`. The
import writes exactly these conversions for a MOV, an arithmetic box or
a CPT that narrows, so a brownfield rung round-trips. Any other
conversion in a ladder assignment is still refused by `logix/data-op`
(`logix/operand` in a compare).

**Verified on Echo** (v38, 1756-L85E emulation, 2026-10-14, #297): the
written conversions computed nautilus's value for 271 of 279 results over
9 input sets; the other 8 were a `3.7` input's float32 rounding. What the
controller does:

| Question | Observed |
|---|---|
| a REAL stored into DINT / INT / SINT | rounds **halves to even**, at run time and for literals: 2.5 → 2, 3.5 → 4, −2.5 → −2, −3.5 → −4, 0.5 → 0, 1.5 → 2, −0.5 → 0, −1.5 → −2 (nautilus's `REAL_TO_DINT`) |
| `MOD` of a negative DINT | truncated, the dividend's sign: −70000 MOD 65536 = −4464, −7 MOD 3 = −1, 7 MOD −3 = 1 (as nautilus) |
| `AND` / `XOR` on a DINT | bitwise: 70000 AND 65535 = 4464, 5 XOR 3 = 6; `((v AND 65535) XOR 32768) - 32768` wraps to INT for every value tried (the writer keeps the `MOD` form) |
| `(v * 1.0)`, a DINT compared with a REAL | the integer becomes REAL (float32: 16777217 = 16777216.0 is TRUE) |
| `TRUNC` of a REAL into a DINT | toward zero (2.7 → 2, −2.7 → −2) |
| a REAL out of range | rounded, then **wrapped** to the target's low bits, never saturated: 3e9 → DINT −1294967296, 2^31 → −2^31, 1e20 → 0, 40000.5 → INT −25536. With the writer's `ReportMinorOverflow="false"` nothing is logged and the controller keeps running; `S:V` cannot be read in ST. nautilus faults the scan |
| an integer `/` beside a REAL | divided **in REAL** once the destination or a comparison partner is REAL: `r := a / 2` is 3.5 for a = 7 (nautilus 3) — #361, below |

### Follow-up — REAL → integer as a store, integer `/` beside a REAL (2026-10-14, #361)

With ties-to-even verified, `REAL_TO_DINT` / `_INT` / `_SINT`, `TO_<int>`
of a REAL and `REAL_TO_TIME` are written as a plain store when the
conversion is the whole right-hand side of an assignment to a tag of the
conversion's own type. Anything else stays refused: inside an expression
(Logix rounds only as it stores), into a narrower or wider tag
(`i := REAL_TO_DINT(r)` with `i` INT: nautilus stores the rounded DINT
unwrapped, the store would round and wrap to 16 bits; the fix is a DINT
tag and `DINT_TO_INT`), and from an LREAL (unverified). Out of range the
general divergence applies: Logix wraps silently, nautilus faults; a
`REAL_TO_TIME` beyond a DINT of milliseconds (±24.8 days) wraps on the
controller, where nautilus's TIME is 64 bits.

An integer `/` whose quotient meets a REAL — stored into a REAL or LREAL,
computed beside a REAL operand, or compared with one — is refused with
`logix/conversion`, naming the fix: compute the quotient into a DINT tag
first (or divide by a REAL if a REAL quotient is meant). A division whose
own operand is REAL is a REAL division in nautilus too and is written as
before; an all-integer division (an integer destination, an integer
comparison partner, an array index) is written unchanged. Ladder's `DIV`
and `CPT` with a REAL destination are not covered here (unverified).

### Follow-up — integer `/` on a rung, expressions as AOI inputs in ST (2026-10-14, #368)

**Verified on Echo** (ws-test, v38, 1756-L85E emulation), DINT `a`, `b`,
`c`, REAL `x`, results read over EtherNet/IP with `naut test --target
logix`:

| Rung (as written) | a, b, c, x = 7, 2, 3, 3.25 | −7, 2, 3, −3.25 | 11, 4, 1, 2.5 | nautilus |
|---|---|---|---|---|
| `DIV(a,b,rDiv)`, REAL destination | **3.5** | **−3.5** | **2.75** | 3, −3, 2 |
| `DIV(a,2,rLit)`, REAL destination | **3.5** | **−3.5** | **5.5** | 3, −3, 5 |
| `CPT(rCpt,(a / b) + c)`, REAL destination | **6.5** | **−0.5** | **3.75** | 6, 0, 3 |
| `CPT(rCptX,(a / b) + x)` | **6.75** | **−6.75** | **5.25** | 6.25, −6.25, 4.5 |
| `CMP(a / b > x)` | **TRUE** | **FALSE** | **TRUE** | FALSE, TRUE, FALSE |
| `DIV` / `CPT` into a DINT, `CMP(a / b > c)` | 3, 6, FALSE | −3, 0, FALSE | 2, 3, TRUE | the same |

So a rung divides integers in REAL in the same places ST does. The writer
now refuses with `logix/conversion` an integer `/` in a DIV or CPT into a
REAL or LREAL, in a CPT with a REAL operand, and in a CMP against a REAL
(`logix/writer/data.go`, typed as ST through `realDivision` in
`convert.go`); into an integer, or against an integer, it is written as
before.

**An integer `/` bound to a REAL input of an AOI in an ST call** has no
run-time divergence to measure: the call does not build. The v38 SDK
refuses any expression as an Add-On Instruction operand in ST —
`PassR(p1, (a / b));`, `PassR(p1, a / b);`, `PassR(p1, (a));` and
`PassR(p1, -a);` all fail the build with `RxSTX_E_UNEXPECTED_TOKEN` —
while `a`, `p2.y`, `-5`, `2.5` and `3` build. The writer emitted the
expression form, so a project with one passed `naut check` and failed at
the SDK build (§7a, "errors surface late"). `logix/fb-pin` now refuses an
input bound to an operator expression or to anything that is not written
as a tag path or a number; a negative literal is written `-5` (not
`- (5)`), and the input counts as a store into the parameter, so
`DINT_TO_REAL(a)` into a REAL input is written as `a`. Not built: an
element with a tag index (`arr[k]`), which the writer accepts.

**The conformance set (#255's last item).** Two projects under
`logix/writer/testdata/conformance/`, run on Echo with `naut test --target
logix` from a copy whose target block names ws-test:

| project | scenarios | nautilus | Echo |
|---|---|---|---|
| `conversions` (ST): every written class into its own tag — the narrowing wraps, widening, TIME as DINT ms, int → REAL / LREAL, BOOL both ways, TRUNC, REAL → DINT / INT / SINT / TIME as a store, a comparison and an index of their own, `xa / 2` into a DINT and through it into a REAL, and three user-block inputs (a DINT tag into a REAL input, `DINT_TO_REAL(xd)`, `REAL_TO_DINT(xr)` into a DINT input) | 12 | pass | **12 of 12 pass**, exact |
| `division` (ladder): DIV and CPT into a DINT and an INT, a CMP against a DINT, the quotient through a DINT tag into a REAL (MOVE), a REAL divided by a DINT, that tag compared with a REAL | 4 | pass | **4 of 4 pass** |

The REAL inputs are exact in float32 (2.5, 3.75, 100.25, …), so the
#297 run's 8 float32 differences of a `3.7` input do not recur, and every
assertion is an exact comparison. The ties 2.5, −2.5, 3.5, −3.5, 1.5,
0.5 and −0.5 are among the inputs, so the user-block input
`REAL_TO_DINT(xr)` shows that the copy into an AOI parameter rounds halves
to even like an assignment. An INT store out of range is not asserted
(`division`'s large quotient): the controller wraps where nautilus keeps
the value (`st-int-width`), which is not a conversion question.

### Round trip on the yardstick (2026-10-14, #276)

The video beat: import the public yardstick, show the project it writes,
write it back, diff. Run offline, pure Go, no controller, from a
directory holding a copy of `examples/batch-skid/line/Line.L5X`:

```sh
naut logix import --project line Line.L5X
#   wrote line/MainProgram.ld, line/lib/logix_types.st,
#   line/nautilus.yaml, line/tags/logix.yaml
#   1 ladder routine, 1 complete; 5 rungs, 5 carried
naut check line                       # clean: 0 errors, 0 warnings
naut logix write -o back.L5X line/MainProgram.ld
naut logix normalize --logic --check back.L5X Line.L5X
```

**On main before this change the flow did not hold.** `naut logix write`
ignored the imported `target: logix` and the tag file, so the write-back
was controller `MainProgram` on a 1756-L85E at 38.11, routine
`MainRoutine`, and every tag at zero with no description: `Line_Ready`
1 → 0, `Line_TankLevel` 20.0 → 0.0, `Line_Status.Mode` 1 → 0. And
`naut check` warned on the fresh import (the timer, moved into the
program, was also left in the tag file). Fixed here:

- `naut logix write` on a project's task program writes what `naut logix
  deploy` would send: controller, processor, revision, program, routine
  and task from `target: logix`, the rate from the task, each tag's
  `init:`, `desc:` and `alias:` from the tag file. A flag given still
  overrides.
- The import records `routine:` (when it is not `MainRoutine`), `program:`
  (when the Logix name is not an identifier) and the new
  `continuous: true` for a CONTINUOUS task, which `write` and `deploy`
  honour instead of turning the task PERIODIC at the 10 ms nautilus scan.
- A controller-scoped TIMER/COUNTER the import declares as a program
  instance is left out of the tag file (unless some program also names it
  as a tag), so the imported project checks clean. (The follow-up below
  lists it under `target.logix.controller-scoped` instead.)
- `naut logix normalize --check` gained `--logic`: drift `--logic`'s
  comparison (programs, routines, rung text and comments, tag names,
  scopes and types; never values) between two files on disk, each
  difference listed. A byte or normalized comparison of a write-back with
  a lean export can never be equal (below), so this is the diff the beat
  shows.

**Outcome on #276: equivalent, not identical, with four logic
differences, each named** — rung 0's `LES(` came back `LT(`, `AcceptTmr`
came back program-scoped (`MainProgram.AcceptTmr`) and without its
description, and `Line_BatchId`, which no routine names, was not written.
Each became an issue; all three are closed by the follow-up below.

**Follow-up (2026-10-14, #370, #371, #372): the same logic, no
differences.** `TestLogixRoundTripYardstickCLI` in `cmd/naut` pins the
exact output; any difference fails it:

```
Line.L5X and back.L5X have the same logic
```

| was | fix | issue |
| --- | --- | --- |
| rung 0 `LES(` → `LT(` (spelling: `LES` is the editor caption, `LT` what an export carries, logix-target.md's display/neutral-text split) | The example itself was wrong: on the v38 SDK (2026-10-14, #377) `LT(a,c)` imports, builds and exports as `LT`, while `LES(a,c)` imports and exports unchanged but fails the build (`RxCMP_E_AUDIT_INVALIDOPTYPE`, DINT and REAL operands alike). Rung 0 of `Line.L5X` now reads `LT(Line_TankLevel,80.0)OTE(Line_Ready);`, a third commit on the file (the README's two-revision interlock story is unchanged). No other example or fixture L5X carries a caption in rung text | #372 |
| `AcceptTmr` controller → program scope, description dropped (semantic for an external client: `AcceptTmr.ACC` moved to `Program:MainProgram.AcceptTmr.ACC`) | A block instance is still a program's in nautilus (a manifest tag cannot be a TON), so the import keeps declaring it in `MainProgram.ld`; it also lists it in the manifest, `target.logix.controller-scoped: [{ name: AcceptTmr, desc: "Accept-handshake settling delay" }]`, and the writer emits a listed program `VAR` as a controller tag with that description. The rungs are unchanged. A listed name that is not a program `VAR` is a diagnostic, never ignored | #371 |
| `Line_BatchId` not written (semantic: the skid writes it over EtherNet/IP; no routine names it and the writer emitted only the tags a program names) | The import sets `target.logix.all-tags: true` — its tag file is the controller's tag set — and the writer then emits every typed manifest tag it can carry, named or not. The tag file now types each tag whose `init:` does not already say its type (`Line_BatchId`: `type: DINT`, since `init: 0` alone is a REAL); a tag with no nautilus type (`MESSAGE`, a multi-dimensional array, an unnamed TIMER) stays in the tag file as documentation, and one whose type the v1 subset lacks (`STRING`, `LINT`, unsigned) stays there too and is skipped by the writer, not refused; the import reports each, because no write can create it. Without `all-tags` a tag no program names is still not written (LGA-001): the opt-in keeps a hand-written project's tag set, and so its `--online` deploys, unchanged | #370 |

Rungs 0–4 are byte-identical, all five rung comments are identical, the
controller (`LineController`, 1769-L33ER, 36.11), program, routine
(`Receive`) and task (`MainTask`, CONTINUOUS) are identical, every one of
the export's eight controller tags comes back at controller scope with its
initial value and description, and the program has no tags, as in the
export.

Outside the logic comparison (the raw `diff` is 129 lines; `normalize
--check` without `--logic` says "differ"):

- **Equivalent — envelope.** The writer emits a whole project where the
  yardstick is a lean export: `RedundancyInfo`, `Security`, `SafetyInfo`,
  `Modules` (the `Local` module), `AddOnInstructionDefinitions`, `CST`,
  `WallClockTime`, `Trends`, `TimeSynchronize`, `EthernetPorts`, more
  `Controller` attributes, `Owner="nautilus"`, a different
  `ExportOptions`, and `ExportDate="(pinned)"`.
- **Equivalent — formatting.** Controller tags in the program's
  declaration order (its `VAR_EXTERNAL`, which the import sorts by name,
  then `AcceptTmr`, then `Line_BatchId`, which no routine names) rather
  than the export's; each value also as `<Data Format="L5K">`; the
  Decorated data on its own lines.
- **Equivalent — the `Local` module.** The yardstick is a lean export
  with no `<Modules>`; the write-back's `Local` module is the
  1769-L33ER's own, from Logix Designer's export of one (#373, below).
  Until #373 (c) it was the 1756-L85E's with the catalog number changed,
  which Logix Designer v38 refuses to import.

**Follow-up (2026-10-14, #373): metadata.** Two of the three named
metadata differences are gone; the third is now said out loud.
`TestLogixRoundTripYardstickCLI` also compares `SoftwareRevision` and each
UDT's description and its members' with the export's.

| was | fix |
| --- | --- |
| `SoftwareRevision="38.01"` for any target (the yardstick's export says `36.00`) | The default is the target's own Logix Designer release, `writer.SoftwareRevisionFor(major)`: `38.01` for v38 (what ECHO1's real exports, `lang/l5x/testdata/demoline.L5X` and its siblings, carry) and `<major>.00` for any other major — `36.00` for v36, which is what the yardstick, a stand-in export, says. The firmware minor (`.11`) is not the software minor, so the controller's `MinorRev` is not used. `naut logix write --software` still overrides it, and now defaults to empty (derived); deploy and serve get the derived value because they leave `SoftwareRevision` unset. A v38 project's output is unchanged, so no golden moved |
| the UDT's description dropped | `naut logix import` writes a UDT's description, and each member's, as the declaration's trailing comment in `lib/logix_types.st` (`LineStatus : STRUCT (* Mode/alarm summary … *)`, `Mode : DINT; (* … *)`) — the IEC habit the LSP already reads for a VAR's description, and the type stays the one place it is said, rather than a manifest field beside it. The writer reads a STRUCT's and member's trailing comment (`//` or `(* … *)`, a multi-line one joined line by line, a `(* @… *)` pragma ignored, a comment on a line of its own not attached) and writes it as the `<DataType>`'s and `<Member>`'s `<Description>`, the member in the element form an export uses. A `*)` or `(*` inside a description is written `* )` / `( *`, so that round trip is lossy by one space |
| the `Local` module is the 1756-L85E's for any processor | Fixed by data, below ("The `Local` module, from Logix Designer itself"). This row first shipped as a warning only (#382): the repo had no CompactLogix export with a `<Modules>` section, and product codes are not guessed |

Needs a controller-side (Logix Designer) check: that v36 Logix Designer
opens a write-back claiming `36.00` (and v38 one claiming `38.01`, as
today); that a UDT and member `<Description>` written in this shape import
and survive a build. (The `Local` module's check is done, below.)

**The `Local` module, from Logix Designer itself (2026-10-14, #373 (c)).**
No export had the data, so Logix Designer was asked for it: on ws-test
(Logix Designer v38.01, the only release installed) logixd's
`POST /v1/create` (`logixd.Client.CreateProject`, the SDK's
`LogixProject.CreateAsync`) made an empty project for each processor at
38.11 and `POST /v1/convert` exported it to a lean L5X. Each export is
committed verbatim in `logix/writer/testdata/local/<catalog>.L5X`; they
are vendor-generated metadata with no client data. The writer's table
(`localModules`) has a row for exactly those processors, and
`TestLocalModulesMatchExports` writes a project for each and compares
it with the export: the `<Modules>` element attribute for attribute,
`<EthernetPorts>`, `<RedundancyInfo>`, and the `<Controller>`
attributes in the export's order (all but the name, the dates and the
`DataExchangeId`).

Three envelopes, by family; within a family only the product code and
the backplane size change:

| family | processors (ProductType 14, ProductCode, backplane `Bus Size`) | `Local` ports | `<EthernetPorts>` | `<Controller>` differs by |
| --- | --- | --- | --- | --- |
| ControlLogix 5580 | 1756-L81E 164, L82E 165, L83E 166, L84E 167, L85E 168 — all 17 | 1 `ICP`, 2 `Ethernet` | `1` | — (the shape the writer always had; DemoLine's L85E agrees) |
| CompactLogix 5370 L3 | 1769-L30ER 106 (9), L30ER-NSE 109 (9), L30ERM 156 (9), L33ER 107 (17), L33ERM 110 (17), L36ERM 108 (31) | 1 `Compact`, 2 `Ethernet` | `1`, `2` | `TimeSlice="20" ShareUnusedTimeSlice="1"` after `MinorRev`; no `AutoDiagsEnabled`/`WebServerEnabled`; `RedundancyInfo` carries `IOMemoryPadPercentage="90" DataTablePadPercentage="50"` |
| CompactLogix 5380 | 5069-L306ER 196 (9), L306ERM 189 (9), L310ER 216 (9), L310ER-NSE 188 (9), L310ERM 190 (9), L320ER 217 (17), L320ERM 192 (17), L330ER 218 (32), L330ERM 193 (32), L340ER 219 (32), L340ERM 194 (32), L350ERM 195 (32), L380ERM 228 (32), L3100ERM 230 (32) | 1 `5069`, 3 and 4 `Ethernet` | `A1`, `A2` | `EtherNetIPMode="A1/A2: Dual-IP"` before `AutoDiagsEnabled` |

The writer still omits the trailing `<OpcUaInfo EnabledPorts=""/>`
(5580, 5380) and `<EthernetNetwork …/>` (5370, DLR supervisor defaults)
an export ends with, as it always has for the L85E; the import defaults
them.

Not in the table, so still the 1756-L85E's module and the warning:

- **CompactLogix 5370 L1/L2** (1769-L16ER-BB1B 153, L18ER-BB1B 154,
  L18ERM-BB1B 155, L19ER-BB1B 152 on a `PointIO` port; 1769-L24ER-QB1B
  149, L24ER-QBFC1B 150, L27ERM-QBFC1B 151 on `Compact`). v38 creates
  them, but each project carries embedded I/O modules beside `Local`
  (one or three) that the writer does not emit; a `Local` row alone
  would write a controller without its own I/O.
- **CompactLogix 5480**: v38 cannot create one (5069-L46ERMW:
  `RxPTV_E_UNSUPPORTED_PROCESSOR_REV`; 5069-L4100ERW, L4200ERW, L4200ERM:
  "Not supported processor type").
- Safety controllers (GuardLogix, Compact GuardLogix), ControlLogix 5570
  and anything else not tried.

Revision: every row came from a v38 project. The `Local` module's
`Major`/`Minor` are the target's, as before; the product code and ports
are the hardware's, so a v36 target (the yardstick) gets the same row,
but no v36 Logix Designer has opened one (only v38 is installed).

**Build-checked on v38** (ws-test, `naut logix build` of a written L5X:
the SDK imports it, then `BuildAsync`): the batch-skid yardstick,
imported with `naut logix import` and written back with its target
retargeted in a scratch copy to 38.11 and each of 5069-L320ER,
5069-L340ERM, 1769-L33ER, 1769-L36ERM and 1756-L83E, imports with
`<Summary Warnings="0" Errors="0"/>` and builds ("Build succeeded") for
every one. A conversion of the 5069-L320ER and 1769-L33ER write-backs
back to L5X shows Logix Designer kept the written `Local` module and
`<EthernetPorts>` unchanged. The writer before this change (the L85E's
module with the catalog number changed) is refused for both
CompactLogix processors: `XMLSrv_E_IMPORT_ABORTED_NO_CHANGES - The
Import was cancelled due to errors`, so the warning #382 added was
describing a project v38 would not open.

Not yet checked on a controller: the v38 build of the corrected
`Line.L5X` and of a write-back with a controller-scoped `TIMER`.

What the video can say, word for word: *"Import, write back, compare the
logic: no differences — every rung, and every tag where the controller had
it, including a timer at controller scope and a tag only the skid
writes."*

### Follow-up — deploy from the editor, verified on a controller (2026-10-14, #375)

The editor's **Push Online Edit** and **Download Project** commands
(#271) post to `POST /api/logix/deploy` on `naut logix serve --project`
and read its NDJSON stream. Verified on an Echo controller (v38,
1756-L85E emulation) with the DemoLine project
(`logix/writer/testdata/project`, its target pointed at that controller in
a scratch copy), driving the endpoints with the request the extension
sends and running each recorded stream through the extension's own
splitter and summary (`logixDeployLogic.ts`). Live values were read from
`/api/stream` throughout.

| step | wall time | result |
|---|---|---|
| Download, controller in Run, no Program-mode switch | 81 s to the refusal | refused by the SDK after import + build (29 s) and the logic diff (39 s): `RxE_INVALID_START_STATE - Invalid start state.  Internal error.`, shown verbatim with the SDK's status lines |
| Download with *Switch to Program Mode and Download* | 120 s to verified, 173 s to the summary | import + build 33 s, diff 37 s, download 16 s, verify 34 s; the reopened online session 53 s more. Summary: "downloaded to DemoLine and verified in 120 s — the controller is in Program mode" |
| first online edit after the serve starts | 50.2 s | session open 47.2 s, then the edit 3.0 s; 2 rungs replaced, verified, Run kept. Largest gap between stream chunks 47.2 s, well inside undici's 300 s body timeout |
| warm online edits, Run | 1.6 s, 1.5 s | 2 rungs replaced, verified, Run kept |
| online edit that adds a tag | immediate | 422: "the tag set changed (1 difference(s)); an online edit cannot create or retype tags, so a download is needed. Download the project to the controller (nautilus: Download Project to Logix Controller, …)"; the extension reads it as needs-download and offers **Download Project…** |
| that download (the tag added to `nautilus.yaml` after the serve started) | 108 s to verified, 154 s to the summary | the held session closed in 0.6 s, then logixd took the download's own project at once; session reopened in 45 s |
| the next online edit, controller in Program | 2.9 s | 2 rungs replaced, verified, Program kept |

Through the edits the live stream never paused (no gap over 2 s) and the
controller's scan counter kept counting; a rung change (`GE` → `GT` with
`LevelPct` = `HiLevelSP` = 85) turned `HiLevelAlm` off on the stream as the
edit finalized. Across a download, tag reads failed for about 3 s and
then recovered with the project's values (`LevelPct` back to 0); the tag
the download added was not served until the serve restarted, the
documented limitation (the facade browses once at startup).

Found and fixed on the way:

- **The warm session could pick the side program's routine.** With
  `side: heartbeat`, the controller has two `MainRoutine`s
  (`MainProgram` and `Nautilus`), and the session chose one in map order:
  after the first download it held `Nautilus/MainRoutine` and refused the
  next edit with "does not run this program's routine yet". The session
  now keeps every routine from the upload and picks by the program each
  edit writes, and compares only that routine (the side routine had also
  kept an unchanged program from ever reading as "already runs this
  logic").
- **A stopped serve left its online session open** in logixd until the
  idle reaper (30 minutes). Stopping the serve now takes the session
  offline and closes it (checked on the controller: logixd listed the
  session before Ctrl+C and none after).

The buttons themselves: smoke check 19 (`tools/rig/smoke/19-logix-deploy-buttons.sh`)
opens DemoLine's `MainProgram.ld` in the text and the diagram editor and
finds **Push Online Edit** in the title bar and **Download Project** in
the `…` menu, and finds neither on a `naut new` project.

Two things were left as they were, and #379 then changed them: a
download's summary arrived only after the next edit's session had
reopened (about 50 s after "verified", with "opening the online session
for the next edit" on screen meanwhile), and an edit's summary time
counted the edit, not the session open before it. Now the download's
report goes out when it has verified and the session reopens after it,
on the same stream (`ProgramPlane.Warm`), while the plane already takes
the next deploy, which waits for the session and says so; `elapsedMs` is
the whole wait from the request (`deployMs` the deploy's own part); the
stream's headers and a first line go out at once, and a warm edit says
"replacing …" and "verifying …" as it gets there. A plain download
refused with `RxE_INVALID_START_STATE` is a `deploy.StartStateError`: a
409 saying the controller is in Run and to use *Switch to Program Mode
and Download*, `needsProgramMode` for the editor's button, and the SDK's
text as `detail`. Not yet re-measured on a controller.

**An ST routine's warm online edit in Run (#272, measured 2026-10-14 with
#273):** the same endpoint, an ST version of DemoLine
(`MainProgram.st`, 11 lines, one comparison changed per edit), the
session opened after an editor download, the controller then put in Run:

| edit, controller in Run | wall time | result |
|---|---|---|
| first after the mode change | 3.7 s | routine replaced, verified, Run kept |
| second, third, fourth | 1.9 s, 1.9 s, 1.85 s | routine replaced, verified, Run kept |

Phase B measured 4.0 s for an ST edit in Program; in Run the warm ST edit
is under 2 s, about the ladder edit's 1.5 s plus the larger routine
export that verifies it.

### Follow-up — a download keeps the controller's values (2026-10-14, #273)

**Before:** a download sent the project as written, so every tag went back
to its initial value. Checked on Echo (v38, ws-test) with a scratch
DemoLine carrying a nested UDT (`Pump1 : Pump`), a REAL array, a TIMER and
a COUNTER: HiLevelSP 72.5, SpareSP 42, Pump1.Speed 55.5,
Pump1.Status.Mode 3, LevelLog[2] 12.25 and the counter's accumulator 17,
all written over EtherNet/IP with the controller in Run, came back as 85,
5, 40, 0, 0 and 0 after `naut logix deploy --download` of the same
project. The upload the deploy already made carried every one of those
values, in both the L5K and the Decorated form.

**Now:** for a download, the deploy uploads the running controller first
(the upload it made anyway, moved ahead of the import), plans the values
tag by tag (`deploy.PlanValues`, a pure function over the generated
project and the upload's L5X), and splices the controller's `<Data>`
elements into the project before import and build (`ValuePlan.Apply`;
the skeleton and the program partial of the two-step AOI build too).
The rule:

- **kept:** a Base tag with the same name (case-insensitive) and scope
  (controller, or the same program) on both sides, the same data type and
  dimensions, and for a UDT or AOI type the same definition on both sides
  (visible members; AOI parameters and local tags; nested types expanded).
  Its data is copied as the SDK exported it.
- **except a TIMER's or COUNTER's preset,** which is the project's: a
  literal `PT := T#30S` lives only in the tag's PRE, so it is code, and a
  changed one must take effect. The accumulator and status bits are kept.
  (An ST timer is an `FBD_TIMER` whose PRE the routine assigns every scan;
  kept whole.)
- **reset, with the reason reported:** type, dimensions or definition
  changed; constant in the project; no data in the upload.
- **new:** only in the project, at its initial value. **Dropped:** only on
  the controller; reported, removed by the download.

The values are the upload's, taken about 30 s (the import + build) before
the download stops the controller; what the program changes in that window
is not carried. A changed `init:` in `nautilus.yaml` does not override a
kept value. `--reset-values` (CLI), `"resetValues": true` (endpoint) and
`nautilus.logix.resetValuesOnDownload` (extension) start every tag at the
project's value. A build without `--download` reports what a download
would do. Total download time is unchanged (the upload moved, it was not
added): 1 m 57 s to verified, against 2 m 0 s before.

**On Echo** (ws-test, controller in Run with the values above written):

| step | result |
|---|---|
| `naut logix deploy --download --yes --program-mode` of a changed project: a new rung, a new tag `NewSP` (init 7), `SpareSP` DINT → REAL (init 5.0), the timer's `PT` 30 s → 45 s | "tag values: 11 kept from the controller, 1 reset to the project's value (SpareSP: DINT → REAL), 1 new (NewSP)"; verified in 1 m 57 s |
| read back over EtherNet/IP | HiLevelSP 72.5, Pump1.Speed 55.5, Pump1.Status.Mode 3, LevelLog[2] 12.25, counter ACC 17 (PRE 1000): kept. SpareSP 5.0: reset. NewSP 7: initial. Timer PRE 45000: the project's |
| Run, StartPB pulsed | RunCmd sealed in, the counter counted on from the kept 17 to 18, Pump1.Run on (from the kept speed), the scan counter counting |
| download from the editor endpoint (`POST /api/logix/deploy`, Switch to Program Mode) of the project rewritten in ST, after writing HiLevelSP 66, SpareSP 9.5, NewSP 12 | "11 kept, 2 reset (MainProgram.RunTmr: TIMER → FBD_TIMER; MainProgram.Starts: COUNTER → FBD_COUNTER)"; done event `valuesKept: 11` and the two `resets`; HiLevelSP 66, SpareSP 9.5, NewSP 12, Pump1 and LevelLog read back kept |

`TestHardwareDownloadKeepsValuesUnlessReset` (LGA-037, LGA-047) is the
repeatable check: download with values reset (85), write 70, online edit
(70 kept), download (70 kept), download with ResetValues (85). Passed on
Echo in 466 s, after a fix: it held one EtherNet/IP connection across the
downloads, and a download resets every connection open before it, so it
now dials per access.


### Follow-up — the hardware configuration (2026-10-14, #275)

Decision 1 of §6, built as proposed: the project carries Logix Designer's
own `<Modules>` section, and the writer merges it. Alias tags of rack
points (Phase E) now land on modules, so an imported real-shaped program
with I/O writes, builds and downloads.

**Built.**

- `lang/l5x` reads `<Modules>`: each module's identity (`Vendor`,
  `ProductType`, `ProductCode`, `Major`/`Minor`), parent, parent port and
  ports, and the element's own bytes (`Controller.ModulesXML`).
- `target.logix.hardware`: an L5X, relative to the project, whose
  `<Modules>` is the I/O configuration. Any whole-project export works;
  only that section is read. `writer.Options.Hardware` replaces the
  generated `Local` module with it, verbatim (CRLF made LF, which XML
  reads the same). `naut logix write`, `naut logix deploy`, the editor's
  deploy (`naut logix serve --project`) and `naut test --target logix`
  all load it through `loadLogixProject`; a file that does not parse, or
  has no `<Modules>` or no `Local` module, stops them by name. With a
  configuration the unverified-`Local`-module warning (#382) is not
  printed: the module is the export's.
- Rules. `logix/io-module`: an alias whose target is a rack point —
  `Local:1:I.Data.3` (slot 1 of the controller's chassis),
  `Remote1:1:I.Data.0` (slot 1 under the adapter `Remote1`),
  `Drive1:I.Speed` (the module `Drive1`) — needs that module in the
  configuration; the message names the alias, the module, and what the
  configuration has, or, with no configuration, says to add one.
  `logix/hardware`: the configuration's `Local` module is the target's
  processor at the target's revision. Both fire in `naut logix write` and
  deploy, not in `naut check` or the editor's live diagnostics, which do
  not read the manifest's target.
- `naut logix import --project` writes `hardware.L5X` (the controller's
  name, processor and revision around the export's `<Modules>`, with a
  comment saying where it came from) and `hardware: hardware.L5X` when
  the export has a module besides `Local`. A `Local`-only export gets
  none: the writer's table (#373) already reproduces that module.

**Why not stubs first.** The 2026-10-06 spike found that Logix Designer's
import takes a module stub — `Name`, `CatalogNumber`, `Vendor`,
`ProductType`, `ProductCode`, `Major`, `Minor`, `ParentModule`,
`ParentModPortId`, the upstream port's `Address` (slot or IP) and an
`EKey` — and fills in the rest, but that the identity is the key: a
missing code aborts the import (`XMLSrv_E_IMPORT_ABORTED_NO_CHANGES`) and
a wrong one silently makes it a different module (in the spike,
product code 13 came back as a 1756-OA16). A generated module is only as good
as an identity table nobody has a public source for, and a module's
configuration (filters, fault modes, RPI) is the plant's, not a default.
A merged export carries both exactly. So the merge is the primary path,
and generation waits for a table verified module by module.

**Measured on ws-test** (Logix Designer v38.01, an Echo 5580 at v38.11):

| step | result |
|---|---|
| a synthetic 1756-L85E project (`IOLine`: seal-in + flow alarm), aliases `StartPB`/`StopPB` → `Local:1:I.Data.0/1`, `RunCmd` → `Local:2:O.Data.0`, `FlowSw` → `Remote1:1:I.Data.0`, written with no modules | SDK import 0 errors, build fails: `RxTAG_ALIAS_TARGET_NOT_FOUND - The target of this alias was not found` (no tag named) — what `logix/io-module` now says at write time |
| the same with four module stubs: 1756-IB16 (1/7/11, 3.1) in slot 1, 1756-OB16E (1/7/16, 3.1) in slot 2, 1734-AENT (1/12/108, 3.1) `Remote1` at 192.168.1.20 on the controller's Ethernet port, 1734-IB8 (1/7/216, 3.1) in its slot 1, all `EKey State="Disabled"` | import 0 errors, build succeeded; exported, every module has its configuration tag, connections and `ExtendedProperties` filled in, and two catalog numbers gained their series (`1734-AENT/A`, `1734-IB8/C`). That export is `lang/l5x/testdata/ioline.L5X` |
| `naut logix import --project` of it, `naut logix write` back | `hardware.L5X` + `hardware: hardware.L5X`; the write-back imports with 0 errors and builds; converted back to L5X, its `<Modules>` is byte for byte the original export's (module `DataExchangeId`s included); the whole file differs only in dates and the controller's and tags' `DataExchangeId`s |
| `naut logix deploy --download --yes --program-mode` of the imported project | 13 differences (a new tag set), downloaded and verified in 2 m 4 s; `naut logix mode run` → Run. The controller browses `Local:1:C/I`, `Local:2:C/I/O`, `Remote1:I/O`, `Remote1:1:C/I` beside the alias tags. Echo emulates no I/O, so the module connections never open; the controller runs regardless |
| over EtherNet/IP in Run: write `StartPB` 1, then 0, then `StopPB` 1 | `Local:1:I.Data` 1 → 0 → 2 (the alias tags are the module's bits); `RunCmd` 1, sealed in at 1, then 0, and `Local:2:O.Data` follows it bit for bit; `FlowAlm` follows `RunCmd` (`FlowSw` 0) |
| restored | `DemoLine.ACD` downloaded, Run, `naut logix drift` against `DemoLine.L5X`: no drift |

`TestSDKConvertAndBuild/ioline` repeats the import and build against any
logixd (passed on ws-test, 1.9 s import and build). The stub identities
above are verified only in that sense: Logix Designer v38 kept each
catalog number (adding a series) and built. They are the start of a
table for generated stubs, not one.

**Not done.**

- Generated stubs from a manifest description (name, catalog number,
  slot or IP). Needs an identity table verified per catalog number as
  above, and a call on where the description lives (the vendor-neutral
  rule: a `target.logix` key, not the language).
- `naut check` / the editor do not run `logix/io-module`, because they do
  not read `target.logix`.
- The importer still refuses an alias of a whole rack word
  (`Local:1:I.Data`): the export now carries the module's connection
  types (`AB:1756_DI:I:0`), but not their member layout in a non-detailed
  export, so the word's type is still unknown.
- The rule checks the module, not the point: `Local:1:I.Data.40` on a
  16-point module passes it.
- Only a 5580 at v38 has run it; a CompactLogix with embedded I/O
  (1769-L1x/L2x), whose `Local` row #373 left out, should now write from
  its own export's configuration, but no such export has been tried.

### Design — `MSG` as IEC 61131-5 `READ`/`WRITE` on a channel (2026-11-16)

**Contexts:** Languages (`lang/ir` blocks), Runtime (the scan's host), Manifest
(`channels:`), Field drivers (`eip` serves the first channel kind), Logix
target (importer and writer). A new published seam, `io.Channel`, sits beside
`io.Driver`, so `lang/ir` stays free of the network.

**Decided (James, 2026-11-15):** MSG is no longer refused outright. The 117
CIP Data Table Read/Write messages become core `READ`/`WRITE` blocks. They
are IEC 61131-5-shaped and vendor-neutral, and they run on a channel the
manifest declares. The first channel kind is CIP to a Logix peer, on the
existing `eip/logix` client. Read Tag and Write Tag are the same services an
MSG Data Table read/write sends. The importer maps MSG to READ/WRITE plus a
channel, and the writer maps them back to MSG, so a write-back round-trips.
CIP Generic (socket) messages stay refused.

**James's calls (2026-11-16):**

- **Block family:** IEC 61131-5 `READ`/`WRITE`, not the PLCopen UA style.
- **Output timing:** NDR/DONE/ERROR hold until the next REQ rising edge.
- **UDT writes:** a whole UDT goes in one Write Tag, laid out by the peer's
  template.
- **The `Flags` copy:** imported as DONE/ERROR at bits 5/4, with a pragma.

Everything else below is the default, as written.

#### The standard, and the alternative

**IEC 61131-5 (2000), §"Data acquisition".** These pins come from a
**secondary source**, a drawing of the standard's block, because the text
itself is paywalled:

| Block | Inputs | Outputs |
|---|---|---|
| `READ` | `REQ` BOOL, `ID` COMM_CHANNEL, `VAR_1..VAR_n` (STRING or a `REMOTE_VAR` address) | `NDR` BOOL, `ERROR` BOOL, `STATUS` INT, `RD_1..RD_n` ANY |
| `WRITE` | `REQ`, `ID`, `VAR_1..VAR_n`, `SD_1..SD_n` ANY | `DONE` BOOL, `ERROR` BOOL, `STATUS` INT |

- **ID:** `CONNECT` produces it. The 61499 mapping in IEC 61131-3:2007 Annex H
  also lets ID be an implementation-defined path string.
- **No BUSY:** the standard has no BUSY pin.
- **STATUS values:** these are the standard's Table 24, which we have not seen.
- **Edge and timing, unverified:** whether REQ is edge-triggered, and whether
  NDR/DONE pulse or hold, are **not verified against the standard**. Vendors
  that build on it, PLCnext `PCP_READ`/`PDI_READ` and Siemens `GET`/`PUT`,
  start on the REQ rising edge and **pulse NDR for one cycle**. RD_n is valid
  only in that cycle. Logix holds DN and ER until the next trigger.

**The alternative: PLCopen OPC UA Client FBs (OPC 30001 v1.02).** This is the
other vendor-neutral client-block family. Its blocks are `UA_Connect`,
`UA_Disconnect`, `UA_NamespaceGetIndex`, `UA_NodeGetHandle`, `UA_Read`,
`UA_Write` and the newer `UA_ReadList`/`UA_WriteList`.

- **Handshake:** `Execute` acts on a rising edge, and the outputs are
  `Done`/`Busy`/`Error`/`ErrorID` (DWORD).
- **Handles:** connection and node handles are explicit, there is a `Timeout`
  input, and the value is a `Variable` IN_OUT.
- **Fit:** it is OPC UA-shaped, with node handles and namespaces, so a CIP or
  Modbus channel would have to fake both.

**Recommendation:** use 61131-5. Its ID/VAR_n pair is protocol-neutral by
design, which is the property we want.

Sources:

- [control.com, 61131-5 blocks](https://control.com/forums/threads/programming-iec-61131-communication-blocks.5445/)
- [IEC 61131-3 Annex H](https://www.holobloc.com/stds/iec/sc65bwg7tf3/document/annexh_0.0.pdf)
- [PLCnext PCP_READ](https://engineer.plcnext.help/latest/PCP_READ.htm)
- [OPC 30001](https://reference.opcfoundation.org/specs/OPC-30001/full)
- [Rockwell MESSAGE structure](https://www.rockwellautomation.com/en-fi/docs/studio-5000-logix-designer/38-02/contents-ditamap/instruction-set/message-structure.html)

#### What the corpus does with MSG (aggregates only)

**Where the MESSAGE tags are:** 7 of 52 exports have them, 141 tags in all:

- 71 are CIP Data Table Write, 46 are CIP Data Table Read, and 24 are CIP Generic.
- All of them are controller-scoped.
- None of them is inside an AOI.

**Data Table parameters (117):**

- **Element counts:** `RequestedLength` is 1 and `LocalIndex` is 0 on every one,
  so there is one element per message.
- **Elements:** the remote element is a plain tag on 109 and `Tag.Member` on 8.
  The local element is a plain tag on 115 and a member on 2. On 110 messages the
  remote and local names are the same.
- **Local types:** **111 move a whole UDT** and 6 move a REAL. The UDTs have
  10–19 members, all INT or REAL, with no arrays and no nested UDTs.
- **`ConnectionPath`:**
  - `2,<ip>` on 103: out the local Ethernet port to the peer.
  - `2,<ip>,1,<slot>` on 9: the peer is in a backplane slot behind that address.
  - `2,<ip>,2,<ip>` on 5: two hops through a bridge.
- **`ConnectedFlag` and `CacheConnections`:** `ConnectedFlag` is 2 on 100 and 1
  on 17. `CacheConnections` appears only with 1: 15 TRUE and 2 FALSE. We do not
  interpret these values; they are carried verbatim.
- **Constant on all 117:** `CommTypeCode` is 0, and `LargePacketUsage` is absent.
- **Peers per export:** 32, 32, 27 and 1, so the importer may generate up to
  32 channels for one export.

**MSG instructions (116 Data Table rungs, all ladder, none in ST):**

- **Uses per tag:** every Data Table tag but one is used by exactly one MSG.
  One Read tag and 8 Generic tags are never used.
- **Round-robin poll, 109 rungs:**
  `XIC(step.k) [XIC(enabled) MSG(M), …, [XIC(M.DN), XIC(M.ER)] TON(dwell),
  [XIC(dwell.DN), XIO(enabled)] OTL(step.k+1) OTU(step.k)]`.
  Each message is one step of a sequencer that polls its peers one at a time.
  It waits on DN or ER, dwells, then advances. **The TON needs DN/ER held**
  while the rung stays true.
- **Periodic, 5 rungs:** `[XIC(T.DN) [MSG(M), RES(T), MOVE(..)], TON(T)]`. A
  free-running timer re-triggers the message.
- **Self-retrigger, 2 rungs:** `XIO(M.EN) MSG(M)`, with DN toggling a bit.

**Status members, across all MSG tags:**

- `.DN` is read 135 times and `.ER` 133 times, all in XIC.
- `.EN` is read 2 times, in XIO.
- `.ERR`, `.EXERR` and `.ST` are never read.
- **`MOVE(M.Flags, D)` appears 109 times**, one per polled message, into a
  plain INT (one into a DINT). D is then read only as `D.5` (108) and `D.4` (107).
- Rockwell's MESSAGE docs place ER at bit 4 and DN at bit 5. The table there
  is garbled, so **confirm this on a controller** before the importer relies on
  it. The other Generic-only members (`.Path`, `.Instance`, `.REQ_LEN`) sit on
  the refused socket messages.

#### The blocks

```iecst
VAR  poll : READ;  push : WRITE;  END_VAR

poll(REQ := step.3, ID := 'line2', VAR_1 := 'Line2Status', RD_1 := Line2Status);
push(REQ := sendNow, ID := 'line2', VAR_1 := 'Setpoints', SD_1 := Setpoints);
```

**READ pins:**

- Inputs: `REQ` BOOL, `ID` STRING, `VAR_1` STRING.
- In-out: `RD_1` ANY.
- Outputs: `NDR`, `ERROR`, `BUSY` (all BOOL), `STATUS` INT.

**WRITE pins:**

- Inputs: `REQ`, `ID`, `VAR_1`, `SD_1` ANY.
- Outputs: `DONE`, `ERROR`, `BUSY`, `STATUS`.

**Where these pins differ from the standard, and why:**

- **One variable, n = 1:** every corpus message moves one element. The
  extensible `VAR_n` form can follow later without breaking these pins.
- **ID and VAR_1 are STRING constants:** ID names a channel in `nautilus.yaml`,
  in the Annex H style, and `naut check` resolves both. There is no `CONNECT`
  block: the manifest owns connections, as it does for drivers.
- **RD_1 is IN_OUT, not an output:**
  - The standard's output would copy RD_1 to its target on every call, which
    puts zeros in the destination before the first read completes. On a
    retained tag that loses data.
  - As IN_OUT, the block writes the variable only on the call that completes,
    and Logix does the same.
  - PLCopen's `UA_Read` takes the same route with its `Variable` IN_OUT.
- **ANY typing:** the slot takes the bound variable's type, which the IR has no
  equivalent for yet. The lowering pass gets one rule: an ANY slot of a
  built-in FB accepts any l-value and keeps its type per instance.
- **BUSY:** this is an extension, as PLCopen has it, and is needed to say
  "in flight" without Logix's EN.
- **STATUS is INT, as in the standard:**
  - 0 means OK.
  - 1–255 is the CIP general status the peer returned, which is the code Logix
    puts in `.ERR`.
  - Negative values are local: −1 unknown channel, −2 cannot connect, −3
    timeout, −4 the peer's type does not match the local type, −5 channel queue
    full, −6 request lost after a restart or takeover.
  - The standard's Table 24 is unseen. If James has the standard, align with it.

**On the scan (the scan never blocks on the network):**

- **Starting a request:** a REQ rising edge while idle does three things. It
  snapshots SD_1, clears NDR/DONE/ERROR/STATUS, and sets BUSY. Then it **posts**
  the request to the channel: a non-blocking enqueue on a bounded queue, which
  returns a ticket kept in an internal slot. A full queue fails at once with −5.
- **While BUSY:** each call **polls** the ticket without waiting. A REQ edge
  while BUSY is ignored, as on Logix. REQ falling does not cancel the request.
- **Completion:**
  - On the first call that sees it complete, BUSY clears.
  - A READ converts the reply into RD_1's type and writes it whole, then sets
    NDR.
  - A WRITE sets DONE.
  - A failure sets ERROR and STATUS and does not write RD_1 at all, not even
    partly.
- **Data lands between statements, never mid-statement.** It happens at a call
  of the block, so a scan sees all of a reply or none of it. That is stricter
  than Logix, which fills the destination asynchronously during the scan.
- **Output timing, after completion:** this is question 2 below. The
  recommendation is that **NDR/DONE/ERROR hold until the next REQ rising
  edge**, which is Logix's DN/ER behaviour. The corpus timer idiom and the
  `Flags` readers need that. The 61131-5 vendors pulse instead.
- **Plumbing:**
  - `lang/ir` declares a `Channels` Host extension with `Post(id, req)` and
    `Poll(ticket)`, like `WallClock` and `DivZeroCounter`. `runtime` implements
    it over the manifest's `io.Channel`s.
  - A host without it fails a REQ edge with −1. That covers `naut check`'s
    evaluation, the LSP, the playground, and tests without a fake.
  - `naut check` errors when ID names no declared channel.
- **Lifecycle:**
  - **Online edit:** an instance keeps its internal slots by name, so an
    in-flight request completes into the migrated instance.
  - **Restart:** internal slots are not retained, so the block starts idle.
  - **Leader takeover:** the new leader finds a ticket it never issued, and
    gets ERROR with −6.

#### The channel

```yaml
channels:
  - name: line2        # what ID names
    type: cip          # first kind; opcua and modbus later
    host: 192.0.2.12
    slot: 0            # optional: route 1,<slot> behind the address
    timeout: 5s        # per request (default 5s)
    queue: 64          # pending requests before −5 (default 64)
```

**Kinds:**

- **CIP:** one worker goroutine per channel, serial FIFO.
  - Connections: one connected CIP session from `logix.Dial`, opened on the
    first request and kept open, with reconnect and backoff. 32 peers means 32
    idle sessions, which is the same order a Logix MSG cache keeps.
  - VAR_1 is a Logix tag path (`Tag`, `Tag.Member`, `Tag[3]`).
- **OPC UA client (later):** reuses the opcua-client driver's keys (`endpoint`,
  `security`, …). VAR_1 is a NodeId string.
- **Modbus (later):** `host`/`unit`. VAR_1 is a register address.

None of these changes the block. Only the meaning of VAR_1 differs, as
61131-5 intends.

**Whole structures:**

- **Reads:** a READ of a UDT tag returns the structure handle and bytes.
  `Registry.DecodeStructData` already decodes them with the peer's template,
  which is read once per type and cached on the channel. The decoded value is
  matched to RD_1 **by member name and exact elementary type**. A difference is
  −4, with nothing written.
- **Writes:** the client has no structure encoder yet, so this is question 3.
  - The recommendation is one Write Tag of type `0x02A0` + handle, laid out by
    the **peer's template offsets**. That is one service, as MSG sends it, and
    atomic on the peer.
  - This is not the declined "full Logix UDT layout": nautilus never computes
    a layout. The peer reports it, and the transfer is refused if a member
    does not match.
  - The emulator's `writeTag` takes elementary leaves only today, so it gains
    struct writes for the tests.

**Paths:**

- `2,<ip>` becomes `host`.
- `2,<ip>,1,<n>` becomes `host` plus `slot: n`.
- The two-hop `2,<ip>,2,<ip>` (5) is **refused**: `Dial` only routes
  `1,<slot>`. A `route:` key can follow if a site needs it.

#### MSG ↔ READ/WRITE

| Logix | nautilus |
|---|---|
| MESSAGE tag `M`, Data Table Read, + `MSG(M)` on a rung | `M : READ` in the program that uses it; rung power → `REQ`; `RemoteElement` → `VAR_1`; `LocalElement` → `RD_1` |
| … Data Table Write | `M : WRITE`; `LocalElement` → `SD_1` |
| `ConnectionPath` peer | a generated `channels:` entry per distinct peer (named from the MESSAGE tag's common prefix, else `peer1…`); `ID` names it |
| `M.DN` / `M.ER` | `M.NDR` (READ) or `M.DONE` (WRITE) / `M.ERROR` |
| `M.ERR` | `M.STATUS` (positive codes; not used in the corpus) |
| `MOVE(M.Flags, D)` | question 4: recommended `D := DONE/NDR at bit 5, ERROR at bit 4, all else 0`, carried with `(* @logix MSG.Flags *)` and folded back to the MOVE |
| `ConnectedFlag`, `CacheConnections` when not the majority (2, absent) | `(* @logix MSG connected=1 cache=TRUE *)` on the instance declaration; the writer emits them verbatim |
| controller scope of the MESSAGE tag | the instance lives in its one using program; the pragma records `controller` so the write-back restores the scope |

**Refused, each with its reason:**

- CIP Generic messages, which are socket and other services (24 tags).
- The two-hop path (5).
- `M.EN` (2 rungs): Logix's EN is "rung true, or in flight", which no pin
  states. Mapping it to `REQ OR BUSY` cannot be written back.
- One MESSAGE instance used by more than one MSG.
- A MESSAGE tag used from two programs.
- `RequestedLength` > 1, or a non-zero `LocalIndex` (none in the corpus).
- Reading any other MESSAGE member (`.EW`, `.ST`, `.TO`, …).
- A MESSAGE tag no MSG uses (1 Data Table + 8 Generic) is reported and not
  carried.

**Write-back:**

- **LD:** a READ/WRITE box on a rung becomes `MSG(M)` in the same position.
  `M` becomes a MESSAGE tag whose `MessageParameters` are:
  - `MessageType` from the block.
  - `RemoteElement` = VAR_1, and `LocalElement` = the RD_1/SD_1 binding.
  - `RequestedLength=1`, `LocalIndex=0`, `CommTypeCode=0`.
  - `ConnectionPath` = `2, <host>` plus `, 1, <slot>` when `slot:` is set.
  - `ConnectedFlag`/`CacheConnections` from the pragma, else the corpus
    majority (`ConnectedFlag="2"`, no cache attribute).
- **Contacts:** `NDR`/`DONE` become `.DN` and `ERROR` becomes `.ER`. BUSY
  becomes `XIC(M.EN) XIO(M.DN) XIO(M.ER)`.
- **The writer refuses:**
  - READ/WRITE in ST or FBD, because Logix ST's MSG has no rung condition and
    the corpus has none.
  - A non-constant ID or VAR_1.
  - A non-`cip` channel.
  - An RD_1/SD_1 binding that is not a tag or member.
- **Authored in nautilus:** blocks written in nautilus take the same path,
  which is how AB development stays nautilus-first.

#### Tests

- **Every PR, pure Go:**
  - `lang/ir`, with a fake `Channels`: edge start, ignored edge while busy,
    held or pulsed outputs, an error leaving RD_1 untouched, an unknown channel.
    There is also a fake that never completes, and the scan time stays bounded.
  - `eip` channel against `eip/logixserver`: elementary values and a flat
    INT/REAL UDT both ways. Peer errors pass through as STATUS 4/5. A mismatched
    UDT gives −4. Reconnect after the server restarts.
  - Runtime: `naut run` with a `channels:` entry pointed at a black-hole address,
    with scan lateness unchanged. That makes the "never blocks" claim a test
    that can fail.
- **Importer and writer:**
  - Fixtures with **synthetic names** for the three rung idioms and the `Flags`
    mirror.
  - Structural round trip: `MessageParameters` identical.
  - A corpus rerun reported as counts before and after.
- **SDK import and build on ws-test** (`logixd` on ws-test, since echo1's SDK
  is broken): the written MESSAGE tags and MSG rungs import and build.
- **On a controller:**
  - nautilus READ/WRITE against Echo as the CIP peer, on both elementary and
    UDT tags.
  - **Open:** whether Echo executes MSG itself. If it does, a written-back
    poll rung runs between two Echo controllers. It also settles the `Flags`
    bit positions (DN = 5, ER = 4) and the `ConnectedFlag` meanings by reading
    them back.
  - Otherwise those wait for real hardware, and the importer's `Flags` mapping
    ships marked unverified.

#### Slices (one PR each, after James's calls)

1. **Blocks, runtime and CIP channel.**
   - `READ`/`WRITE` built-ins, ANY IN_OUT slots, the `Channels` Host
     extension, `io.Channel`, and `channels:` with `naut check`.
   - The CIP channel with struct read/write against templates, and emulator
     struct writes.
   - May split into 1a (language and runtime on a fake channel) and 1b (CIP).
2. **Importer:** MSG → READ/WRITE + channels + pragmas + `Flags`, with the
   refusals above.
3. **Writer:** READ/WRITE → MSG + MESSAGE tags, round trip and SDK build.

**Slice 1, built (2026-11-16).**

- **Blocks:** `READ`/`WRITE` are in `lang/ir/builtins_comm.go`. `AnyT` marks an
  ANY pin: the lowering skips the type check and the VM skips coercion.
  `ir.Channels` is the Host extension, and the scan view forwards it.
- **Runtime:** `runtime/channels.go` holds a hub with one worker per channel,
  ticketed and non-blocking. `Run` closes the channels, and closing cancels
  any request in flight.
- **Manifest:** `channels:` is in `internal/project`. The browser build stubs
  each channel to report −2.
- **CIP:** `eip/channel.go` is the CIP channel. The client gains
  `WriteTagStruct`, the emulator gains struct writes, and `CIPError.CommStatus`
  carries the general status.
- **Check:** `naut check` errors on a constant ID that no channel declares.

**Departures from the design above:**

- **Where the seam lives:** it is `runtime.ChannelIO`, not `io.Channel`, because
  the context map forbids `io` importing `lang/ir`. A protocol package satisfies
  it structurally, so `eip` still does not import `runtime`. An error carries
  its STATUS through a `CommStatus() int` method.
- **Leader takeover:** it resets program frames, so a block in flight starts
  idle rather than reporting −6.

**Review round (#699):**

- **Integer width:** a hidden internal on each block carries the bound
  variable's declared elementary type (`ir.AnyTypeSlot`), so the channel
  tells INT from DINT on a bare variable. A value outside the peer's range is
  refused, never truncated.
- **Transfers:** whole arrays move both ways, and a large value goes as Write
  Tag Fragmented.
- **Learning the peer:** a CIP refusal makes the channel learn the peer again.
  The tag list is uploaded one scope at a time, on a 30 s budget of its own.
- **Lookup:** channel names match case-insensitively everywhere, and
  `naut check` walks user FUNCTION_BLOCK bodies.
- **Shutdown:** close finishes queued requests and refuses later posts.

**Slice 2, the importer (2026-11-16).**

**The mapping:** `logix/importer/msg.go` turns each CIP Data Table
MESSAGE into a `READ` or `WRITE` instance of the same name. It is declared
in the program whose MSG sends it and written back at controller scope
through `target.logix.controller-scoped`, like a moved timer. The MSG itself
becomes the block in the rung, and inside an output branch it is hoisted
like a timer. Each peer becomes a `channels:` entry named
`cip_<host>[_s<slot>]`.

**Departure from the design:** the connection flags do not ride on a pragma
on the instance declaration. They go to `target.logix.messages` in
`nautilus.yaml`, beside the other Logix-only settings, and only when they
differ from the defaults: `ConnectedFlag` other than 2, any
`CacheConnections`, or a `ConnectionPath` spelled other than the canonical
`2, host[, 1, slot]`. The `Flags` copy becomes
`{ D := SEL(M.NDR, 0, 32) + SEL(M.ERROR, 0, 16) (* @logix MSG.Flags *) }`.

**Also fixed:** an output branch that ends a rung used to drop every leg
without a coil, silently. That covered timers and data boxes, and now MSG.
Such a leg is a rung of its own now, in order.

**Found and filed:** #700. A preset computed in the logic
(`MUL(sp,1000,T.PRE)`) imports as a write that the timer call's literal `PT`
overrides on every scan. About 2,240 corpus rungs have this shape, mostly
in AOIs.

**Corpus (aggregates, after the #701 review):**

- **Rungs:** 29,792 → 29,869 (98.0 → 98.3 %).
- **MESSAGE refusals:** the "type" bucket went from 156 to 14. "MSG" is 62,
  up from 21: CIP Generic, the two-hop path, `.EN`, and Generic legs the
  dropped-leg fix makes visible.
- **The poll idiom:** the round-robin rung and its `Flags` copy import.
  Five periodic-timer rungs stop at `RES`, which has no form yet.
- **Routines and write-backs:** complete ladder routines stay at 800, and
  write-backs at 68 identical / 19 differ of 87.

**Review round (#701):**

- **One-shots and AFI:** a one-shot or AFI leg of a final branch stays a
  condition, as before. Counting it as acting had cost 2 routines.
- **Shared messages:** a MESSAGE sent by two MSGs, or read from a program
  that does not send it, is refused.
- **Shadowed local element:** a controller MESSAGE whose local element a
  program tag shadows is refused.
- **`target.logix.messages`:** it holds entries only for messages the import
  carries, one per name.
- **Addresses:** the path's address must parse as an IP.

**Slice 3, the writer (2026-11-16).**

**The mapping:** `logix/writer/msg.go`. A READ or WRITE instance is a
`MESSAGE` tag, and its call on a rung is `MSG(inst)`, which passes its
rung-in on as the block passes power. The MessageParameters are written in
the order Studio exports them. Their sources:

- **From the call:** `VAR_1`, and `RD_1` / `SD_1` as the local element.
- **From the channel:** `2, host[, 1, slot]`.
- **From `target.logix.messages`:** the connection flags, or a path
  spelled otherwise.

The `@logix MSG.Flags` box folds back to `MOVE(M.Flags,D)`. `writer.Options`
gains `Channels` and `Messages`, filled from the manifest by deploy,
`naut logix deploy` and `naut logix write`. `Check` has no manifest in hand,
so it leaves channel IDs to `naut check`.

**The writer refuses:**

- READ/WRITE in ST.
- A non-literal ID or VAR_1.
- A non-`cip` channel.
- An expression as RD_1 / SD_1.
- An output capture.
- `BUSY`, which has no MESSAGE bit; the design's EN/DN/ER spelling of it is
  not written.
- A second call of an instance with other parameters.
- An instance no rung calls.

**Review round (#702):**

- **One call:** an instance has one call, so even an identical second call
  is refused.
- **Where READ/WRITE is refused:** inside a FUNCTION_BLOCK, because an
  Add-On Instruction cannot hold a MESSAGE tag, and in ST, with the message
  rule rather than the subset list.
- **Arrays:** a whole array is `RequestedLength` n from `[0]`.
- **Ports:** a channel on another port is refused, since an MSG path cannot
  name a port.
- **Redeploy:** a redeploy that keeps values takes a MESSAGE's parameters
  from the project, never the controller's old copy.
- **`naut logix write`:** it reads `channels:` for any program of the
  project.
- **`lang/ld`'s variable scan:** it now runs on the shared `hdrvars` scanner,
  one definition with SFC and FBD, so `VAR CONSTANT` keeps its section.
- **Documented:** `slot:` absent means the controller at `host` itself.

**Measured:** the importer's fixture round-trips every MessageParameters
attribute identically, both the default flags and `ConnectedFlag="1"` with
`CacheConnections`. The poll rung comes back as the three rungs the import
split it into.

**Also fixed:** `lang/ld`'s variable scan missed a one-line
`VAR t1 : TON; END_VAR`. The writer then took the instance as undeclared and
dropped its call without a diagnostic.

**Not yet done:**

- **The SDK build of a written MSG** (logixd on ws-test).
- **A run on a controller.** That would also settle the `Flags` bits and the
  `ConnectedFlag` meanings.

**Limits, for later:**

- WRITE does not encode a Logix `STRING`.
- The emulator serves whole structures only at a root symbol, so a
  member-path transfer (`Tag.Member`) is covered by a controller, not by CI.

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
- Echo: `ssh echo1` (192.0.2.20; CIP on :44818 when Tailscale refuses).
  Fast Startup is off; restart with `incus restart echo-vm --timeout 300`.
  Don't change the controller's mode without asking James.

## 10. Caveats the videos quote

The one list of what the Logix authoring target does not do, for the
video scripts' caveats beat (#277). A script reads the **Caveat** column
as written. Keeping it current is standing practice: when a caveat
closes, move it to *Closed since* with the PR and date; when one opens,
add it here and to the guide's [What it does not do
yet](../../website/src/content/docs/guides/logix-authoring.md#what-it-does-not-do-yet).
Last checked against `main` on 2026-10-14.

**Open**

| # | Caveat | Evidence | Status |
|---|---|---|---|
| 1 | Every behaviour test has run on one emulated controller, a ControlLogix 5580 at firmware v38 on FactoryTalk Logix Echo; nothing has run on real hardware yet. | §7b Echo runs (#297, #368, #375, #273); LGA-037 | open, 2026-10-14 |
| 2 | For a processor nautilus has no Logix Designer export for, such as a CompactLogix 5370 L1 or L2, a CompactLogix 5480, a safety controller or a ControlLogix 5570, the write warns that Logix Designer may refuse the project, unless the project carries its own hardware configuration. | `writer.LocalModuleWarning`; `TestLocalModule`; LGA-046; #373 | open, 2026-10-14 |
| 3 | A Logix project is one task running one ladder or structured text program; multiple tasks, function block diagrams and SFC are refused. | `TestLogixTargetRefusesSFCAndASecondTask`; LGA-036 | open, 2026-10-14 |
| 4 | The target has no STRING, unsigned, bit-string, LINT or LTIME types, and refuses a program that uses one. | `logix/type`, `logix/conversion`; `TestRejections` (`type`, `unsigned`) | open, 2026-10-14 |
| 5 | The IEC TP, CTD and CTUD blocks and edge-trigger instances are refused, because their behaviour differs from the Logix instructions; TON, TOF and CTU are written. | `logix/fb`; `TestRejections` (`TP block`, `CTUD block`) | open, 2026-10-14 |
| 6 | In structured text, MIN, MAX, LIMIT, SEL, MUX, ATAN2, RETURN and CONTINUE are refused; write the IF or CASE instead. | `logix/st`; `TestStructuredTextRejections` | open, 2026-10-14 |
| 7 | User functions are refused, since Logix has none; user function blocks become Add-On Instructions. | `logix/function-block`, `logix/fb`; `TestUserFunctionsAndBlockArraysAreRefused` | open, 2026-10-14 |
| 8 | An array of timers or counters is written as one Logix array, but an array of your own function block is refused; declare one instance per element. | `logix/array-shape`; `TestTimerInstanceArrayOnLogix`, `TestUserFunctionsAndBlockArraysAreRefused` | open, 2026-10-14 |
| 9 | A type conversion the controller would compute differently is refused, and so is an integer division whose result meets a REAL; compute the quotient into a DINT tag first. | `logix/conversion`; #255, #361, #368; conformance projects `conversions` and `division` (§7b) | open, 2026-10-14 |
| 10 | In structured text, each input of an Add-On Instruction call must be a tag or a number, because Logix Designer will not build an expression there. | `logix/fb-pin`; #368 | open, 2026-10-14 |
| 11 | A REAL is 32 bits on the controller and 64 bits in nautilus, so results can differ in the last digits; tests compare REALs with a tolerance. | #297 (8 of 279 results, all float32 rounding) | open, 2026-10-14 |
| 12 | A REAL too large for the integer it is stored into wraps silently on the controller, where nautilus faults the scan. | #297 out-of-range row (§7b); #361 | open, 2026-10-14 |
| 13 | nautilus does not wrap an integer at its declared width and the controller does, so an overflowing INT gives a different value on each. | conformance `st-int-width`; #368 note (§7b) | closed, 2026-11-17: a store wraps at its declared width on both (#782) |
| 14 | A download still stops the controller; it keeps tag values it can match, but reads them about thirty seconds before the stop, so a value the program changes in that window is not carried. | #273; LGA-047; `deploy.PlanValues` | open, 2026-10-14 |
| 15 | A tag that a download adds does not appear in live values until `naut logix serve` is restarted. | #375 Echo run (§7b); `logix/facade` browses the controller once at startup | open, 2026-10-14 |
| 16 | nautilus does not create I/O modules itself: a program that uses rack I/O takes its modules from a Logix Designer export kept in the project, which the import writes, so a new module is added in Logix Designer first. | #275; LGA-052, LGA-053; §7b "Follow-up — the hardware configuration" | open, 2026-10-14 |
| 17 | Import keeps a rung it cannot express as a comment and reports it; message instructions and the buffer copies that feed them are the largest gap. | corpus of 52 exports: 98.0% of rungs, 83% of routines whole (guide); like-for-like COP/CPS import since 2026-11-15 | open, 2026-11-15 |
| 18 | Commercial licence terms (automation interface, pooling) are not settled. | §7b Phase A licence check (§1.4.10, §1.4.7) | open, 2026-10-14 |

**Closed since the issue was filed (2026-10-10)**

| Was | Now | Evidence | Closed |
|---|---|---|---|
| a download resets tag values | a download keeps the values of every tag it can match (`--reset-values` to opt out) | #273, #387; LGA-047, LGA-048; `TestHardwareDownloadKeepsValuesUnlessReset` on Echo | 2026-10-14 |
| the target's rules show only with #239 | they squiggle in the editor as you type | #239 | 2026-10-13 |
| no deploy from the editor | online edit and download from VS Code, verified on Echo | #375; LGA-041, LGA-042 | 2026-10-14 |
| the import round trip differs | the public yardstick writes back with no logic differences | #380; LGA-044; `TestLogixRoundTripYardstickCLI` | 2026-10-14 |
| one processor's `Local` module | Logix Designer's own for ControlLogix 5580, CompactLogix 5370 L3 and 5380; written projects import and build on v38 | #389; LGA-046; `TestLocalModulesMatchExports` | 2026-10-14 |
| a program that uses rack I/O needs a controller project that already has the modules | the project carries the export's I/O modules (`target.logix.hardware`, `hardware.L5X` from the import); an imported program with rack-I/O aliases writes, builds, downloads and runs on Echo, and an alias of a missing module is refused at write time by name | #275; LGA-052, LGA-053; `TestSDKConvertAndBuild/ioline` | 2026-10-14 |
| arrays of block instances | arrays of TON, TOF and CTU are one Logix array tag (user blocks: item 8) | Phase E (§7b); `TestTimerInstanceArrayOnLogix` | 2026-10-03 |
