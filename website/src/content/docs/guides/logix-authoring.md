---
title: Authoring for Logix (experimental)
description: Write Allen-Bradley logic in nautilus — ladder or structured text, in git, tested on the nautilus runtime — and deploy it to a ControlLogix or CompactLogix controller, with Studio 5000 running headless as compiler and loader.
---

:::caution[Experimental]
The Logix target is a proof of concept. The language features it relies on
(`{ y := expr }`, bit access, arrays of instances, `dialect:`, `alias:`)
are ordinary nautilus features, but the writer, `naut logix deploy`,
`naut check --target logix` and `naut test --target logix` may change
shape between releases. It has been verified against one controller
family at one firmware revision (v38, on FactoryTalk Logix Echo). Read
[What it does not do yet](#what-it-does-not-do-yet) before pointing it at
a controller that matters.
:::

> **Keep the hardware. Ditch the IDE.**

The [Allen-Bradley Logix guide](/guides/logix/) keeps Logix as the program
of record and builds the software engineering around it. This guide is the
other direction: **nautilus is where the logic is written**, and the
controller runs what nautilus generated.

- Logic is written, reviewed, diffed and tested in VS Code as nautilus
  source in git.
- Studio 5000 runs **headless behind `logixd`** as compiler, importer and
  loader. Nobody opens it to work; opening it is only ever a way to confirm
  the controller holds what the repo says.
- Live values, set-value and the rung overlay come from the running
  controller over EtherNet/IP the whole time.

## What you need

- `naut` on your machine, any OS.
- For deploying: a [`logixd` agent](/guides/logix/#half-two-driving-a-project--the-logixd-agent)
  on a Windows machine with a licensed Studio 5000 (v37 or later for
  builds), and a controller it can reach through FactoryTalk Linx.
- Nothing else for writing, checking and testing: those are pure Go.

## A project with a Logix target

A Logix project is an ordinary nautilus project with a `target: logix`
section in `nautilus.yaml`:

```yaml
name: demoline
tasks:
  - program: MainProgram.ld
    scan: 100ms
tags:
  - { name: StartPB, role: input, init: false, desc: "P-101 start pushbutton" }
  - { name: StopPB, role: input, init: false, desc: "P-101 stop pushbutton, NC" }
  - { name: LevelPct, role: input, init: 0.0, unit: "%", desc: "T-101 level" }
  - { name: HiLevelSP, role: setpoint, init: 85.0, unit: "%", desc: "LAH-101 setpoint" }
  - { name: RunCmd, role: output, init: false, desc: "P-101 run command" }
  - { name: HiLevelAlm, role: output, init: false, desc: "LAH-101 active" }
target:
  logix:
    controller: DemoLine
    processor: 1756-L85E
    revision: "38.11"
    comm-path: AB_ETH-1\10.0.0.5\Backplane\0   # how logixd reaches it
    host: 10.0.0.5                              # live values, tests
    side:
      heartbeat: Nautilus_Scan                  # exact scan counts in tests
```

```iecst
PROGRAM MainProgram
VAR_EXTERNAL
    StartPB, StopPB, RunCmd, HiLevelAlm : BOOL;
    LevelPct, HiLevelSP : REAL;
END_VAR
LD
  RUNG seal (* P-101 seal-in: Start latches the run command, Stop drops it *)
    [ StartPB | RunCmd ] /StopPB ( RunCmd )

  RUNG alarm (* LAH-101 when the level is at or above the setpoint *)
    GE(LevelPct, HiLevelSP) ( HiLevelAlm )
END_LD
END_PROGRAM
```

The manifest's tags that the program names become controller-scope Logix
tags, with their initial values and descriptions. The `VAR_EXTERNAL` block
is optional: every program sees the project's tags without declaring them
([the tag model](/guides/tag-model/)), and a ladder or ST program that names
one undeclared gets its controller tag all the same, typed from the
manifest. A tag no program names is not written, unless `target.logix`
sets `all-tags: true` (an imported project does): then every manifest
tag with a type (its `type:`, or the one its `init:` implies) is a
controller tag, named or not, except one whose type the target does not
carry (a `STRING`), which is left out. A manifest tag with no
`role:` stops `naut logix deploy` and `naut logix write` with an error
naming the tag, as it stops the runtime. A program's `VAR` become
program tags. The task
becomes a periodic Logix task at the task's scan rate, the program a Logix
program, and the rungs its `MainRoutine`. `program`, `routine` and `task`
under `target.logix` rename them.

The other `target.logix` keys: `slot` (processor slot, default 0), `port`
(EtherNet/IP port, default 44818) and `agent` (the logixd URL;
`NAUTILUS_LOGIXD_URL` overrides it). The logixd token is never in the
manifest: set `NAUTILUS_LOGIXD_TOKEN`.

`side.heartbeat` asks the writer for a second Logix program, `Nautilus`,
scheduled after yours, that counts task scans in the named DINT. It never
touches your routine; `naut test --target logix` waits on it.

## What the target accepts

The Logix target takes **one task running one ladder (`.ld`) or
structured text (`.st`) program**, plus the libraries it uses. FBD and SFC
programs are not in the subset.

| nautilus | on the controller |
|---|---|
| contacts `X` `/X`, branches `[ a \| b ]` | `XIC` `XIO`, `[ , ]`, nesting kept |
| coils `( X )` `( S X )` `( R X )` | `OTE` `OTL` `OTU` |
| edges `+X` `-X`, edge coils `( P X )` `( N X )` | `ONS` `OSF` with generated storage bits |
| compares `GT GE LT LE EQ NE` | the compare instructions; an expression operand becomes `CMP` |
| `{ y := expr }` | `MOVE`, `ADD`, `SUB`, `MUL` or `DIV` for one operation on plain operands, `ABS` for `ABS(x)`, and `CPT` for anything else: `MOD`, unary `-`, `SQRT` (as `SQR`), `**` or `EXPT`, and nested expressions |
| `t:TON` `t:TOF`, `c:CTU` | ladder: `TON` `TOF` `CTU` on `TIMER` / `COUNTER` tags. ST: `TONR(t)` (with `t.Reset := NOT IN`, so it clears when `IN` drops, as a TON does), `TOFR(t)` and `CTUD(c)` on `FBD_TIMER` / `FBD_COUNTER` tags. Both: `t.Q`→`t.DN`, `t.ET`→`t.ACC`, `c.CV`→`c.ACC` |
| `t:TONR` (with `dialect: logix`) | ladder: a `TON` with a `RES(t)` rung ahead of it; ST: `TONR(t);` on an `FBD_TIMER` |
| `FIRST_SCAN()`, `/FIRST_SCAN()` | `XIC(S:FS)`, `XIO(S:FS)`; `S:FS` in ST |
| `clk:LOCAL_TIME()`, `clk.SECOND` | `GSV(WallClockTime,,LocalDateTime,clk[0])` into a `DINT[7]`, `clk[5]` |
| `Word.3` | bit 3 of the word, as Logix spells it |
| `Timers : ARRAY [0..3] OF TON` | one `TIMER[4]` tag |
| BOOL, SINT, INT, DINT, REAL, LREAL | the same types |
| `TIME` | a DINT of milliseconds, where it feeds a preset |
| `t:TON(PT := TO_TIME(ms))`, `ms` a DINT | `MOVE(ms,t.PRE)` ahead of the timer's rung: a preset the logic computes, in milliseconds as `.PRE` is |
| STRUCT types in a library | UDTs |
| enumerations (`TYPE Mode : (Idle := 1, Run := 10);`) | `DINT` tags; a member (`Run`, `Mode#Run`) is its value, `10` — Logix has no enumerated type |
| user FUNCTION_BLOCKs (ladder or ST) | Add-On Instructions |
| ladder `rd:READ(ID := 'line2', VAR_1 := 'Remote', RD_1 := Local)`, `WRITE` likewise with `SD_1` | `MSG(rd)` on a `MESSAGE` tag of type CIP Data Table Read / Write, its path from the `cip` channel `line2` (`2, host[, 1, slot]`); `rd.NDR`/`DONE`→`rd.DN`, `rd.ERROR`→`rd.ER`, `rd.STATUS`→`rd.ERR`. Written back from an import and checked by round trip; not yet run on a controller |
| ST: assignments, IF, CASE, FOR, WHILE, REPEAT, EXIT, operators, maths | Logix ST, close to as written |
| ST conversions (`DINT_TO_INT`, `INT_TO_REAL`, `BOOL_TO_DINT`, `TRUNC_DINT`, …) | Logix that computes the same result: a narrowing wraps in integer arithmetic, an integer meets REAL as `(v * 1.0)` where it would otherwise divide as integers, a BOOL becomes a number through an `IF`, `REAL_TO_DINT(r)` as the whole right-hand side into a DINT is a plain store (the controller rounds halves to even, as nautilus does) |

The language features in that table are documented with the rest of the
language: [the assignment element, bit access, arrays of instances and
dialects](https://github.com/joyautomation/nautilus/blob/main/docs/functions.md).

### Rejected at check time, by name

What the target cannot express is a `naut check` diagnostic that names the
construct and, where Logix has one, the alternative, before anything is
written, never a surprise at download. A few constructs have no Logix
alternative to name (`STRING`, a type that contains itself), and a
binding the compiler cannot read is reported as the compiler reports it:

```text
MainProgram.ld:11:1: logix target: pulse: type TP is not in the Logix v1 subset;
  its IEC load/reset semantics differ from the Logix instruction; ... [logix/type]
MainProgram.ld:18:1: logix target: pulse:TP: not in the Logix v1 subset; ...
  the v1 blocks are TON, TOF and CTU (and TONR with dialect: logix) [logix/fb]
```

A project with a `target: logix` section gets these without asking: in
the editor as you type, once the file compiles, and from `naut check`;
`naut check --target logix` asks for them on any project. Each carries a
stable rule ID, which the editor's Problems panel shows beside the
message:

| Rule | What it enforces |
|---|---|
| `logix/type` | the types in the table above; an AOI takes structures and arrays only as `VAR_IN_OUT` |
| `logix/fb` | no `TP`, `CTD`, `CTUD`, `R_TRIG`/`F_TRIG` instances (IEC and Logix semantics differ) |
| `logix/fb-pin` | timers bind `PT` (TONR also `Reset`), counters `PV` and `R`; the rung's power drives `IN`; in an ST call to a user block each input is a tag, a member or element of one, or a literal (`p(x := a / b)` does not build on Logix; compute it into a tag first) |
| `logix/function-block` | user FUNCTIONs have no Logix form (inline them); an AOI cannot reach a controller tag |
| `logix/var-section` | a program takes no parameters: only `VAR` and `VAR_EXTERNAL` |
| `logix/time` | `TIME` only where it feeds a preset |
| `logix/preset` | a preset is a literal, a declared variable, or `TO_TIME` of an integer variable (milliseconds) |
| `logix/reset` | a counter reset is a plain BOOL reference |
| `logix/block-position` | a `TOF` or `CTU` sits on the rung itself, not inside a branch |
| `logix/array-shape` | arrays start at 0, are one-dimensional; BOOL arrays are a multiple of 32 |
| `logix/array-init` | no array initializers |
| `logix/init` | an initial value is a literal the tag can carry |
| `logix/name` | at most 40 characters, no consecutive or trailing underscores |
| `logix/fn` | function contacts are the compares |
| `logix/operand` | a compare operand is a reference, a literal, or an expression `CMP` can spell |
| `logix/coil-edge` | an edge coil is its rung's only coil |
| `logix/member` | member access only into timers, counters, user blocks and STRUCTs |
| `logix/st` | no `RETURN`, `CONTINUE`, `STRING`, `MIN`/`MAX`/`LIMIT`/`SEL`/`MUX`, `ATAN2`, user calls |
| `logix/data-op` | an assignment Logix has a data instruction or a `CPT` for |
| `logix/conversion` | a conversion the controller would compute differently: REAL to an integer inside an expression or into a tag of another type (as the whole right-hand side into a tag of its own type it is written as a store), an integer `/` whose quotient meets a REAL, in ST or on a rung (`r := a / 2`, `a / 2 > x` with `x` REAL, `{ r := a / 2 }`, `GT(a / 2, x)`: the controller divides in REAL, 3.5 where nautilus gives 3; compute it into a DINT tag first), unsigned, bit-string, `LINT`, `LTIME` and `STRING` conversions, a BOOL or `LREAL` conversion inside an expression, an integer *expression* converted to REAL |
| `logix/io-module` | an alias of a rack point whose module the hardware configuration has (`naut logix write` and deploy only; see [I/O](#io-tag-aliases)) |
| `logix/hardware` | a hardware configuration whose `Local` module is the target's processor at its revision (`naut logix write` and deploy only) |

## Vendor idioms: `dialect: logix`

Some Logix idioms have no IEC block. `dialect: logix` in `nautilus.yaml`
adds blocks with Allen-Bradley semantics, written in nautilus, to every
program in the project. The nautilus runtime runs them, the writer emits
the native instruction, and the importer folds the native idiom back into
them:

```yaml
dialect: logix
```

| Block | Semantics | On the controller |
|---|---|---|
| `TONR` (`IN`, `PT`, `Reset` → `Q`, `ET`) | a TON whose `Reset` clears it while TRUE; `t:TONR(PT := T#1S, Reset := t.Q)` is a free-running pulse | in ladder, a `TON` with a `RES(t)` rung ahead of the timer's rung; in ST, an `FBD_TIMER` run by `TONR(t);`, its `.Reset` TRUE while `Reset` is TRUE or `IN` is FALSE |

The default dialect is `nautilus`, which adds nothing. An idiom with a
standard equivalent needs no dialect: the first-scan flag `S:FS` is
`FIRST_SCAN()` and the controller's calendar, `GSV(WallClockTime,,
LocalDateTime,…)`, is the `LOCAL_TIME` block, both standard in every
project. `LOCAL_TIME`'s millisecond is Logix's microsecond element divided
by 1000, so a ladder program reads it in an ST routine.

## I/O: tag aliases

A Logix program names a rack point through an alias tag. In nautilus that
binding is a tag's `alias:`:

```yaml
tags:
  - { name: StartPB, role: input, alias: "Local:1:I.Data.3" }
```

The program uses `StartPB`. The writer emits an alias tag
(`AliasFor="Local:1:I.Data.3"`). The nautilus runtime ignores the binding,
so the same source runs in simulation. It is the one place a hardware
address lives in the project.

The modules those points live on are the project's **hardware
configuration**: an L5X whose `<Modules>` section holds the controller's
chassis, its I/O modules, and any adapters and the modules under them.
`target.logix.hardware` names it:

```yaml
target:
  logix:
    controller: IOLine
    processor: 1756-L85E
    revision: "38.11"
    hardware: hardware.L5X
```

The writer copies that section into every project it writes, unchanged,
in place of the controller module it would otherwise generate. Nothing in
it is nautilus's: each module's identity, configuration and connections
are what Logix Designer exported. `naut logix import --project` writes
`hardware.L5X` from an export with I/O modules (below); a Logix Designer
export of the whole project works as it is, so changing the hardware is
changing it in Logix Designer and exporting over the file.

An alias of a rack point needs its module in the configuration, and
`naut logix write` and `naut logix deploy` check that before anything
reaches Logix Designer (`logix/io-module`; `naut check` does not read the
hardware configuration): `Local:1:I.Data.3` needs a module in slot 1 of the
controller's chassis, `Remote1:1:I.Data.0` one in slot 1 under the
adapter `Remote1`, `Drive1:I.Speed` a module named `Drive1`. The error
names the alias, the module it needs and the modules the configuration
has; without a hardware configuration it says to add one. (Unchecked,
Logix Designer imports the project and then fails its build with
`RxTAG_ALIAS_TARGET_NOT_FOUND`, naming no tag.) The configuration's
`Local` module must be the target's `processor` at its `revision`
(`logix/hardware`). The check is whether the module is there, not
whether the point exists on it.

Verified on Logix Designer v38 and a 5580 on Logix Echo: a project
imported from an export with two 1756 modules in the controller's chassis
and a 1734 POINT I/O adapter with a module under it wrote back, imported
and built with no errors, downloaded, and ran in Run with its alias tags
driving the module points (Echo emulates no I/O, so nothing answers on
the modules' connections). Exported again from Logix Designer, the
`<Modules>` section was byte for byte the one imported.

## Bringing an existing program in

```bash
naut logix import --project ./line Line.L5X
```

writes a nautilus project from a full L5X export: the manifest (with
`target: logix` filled from the controller, and `dialect: logix` when the
export uses its idioms), the tags with their descriptions, UDTs as IEC
types (a type's and each member's description as the declaration's
trailing comment, `LineStatus : STRUCT (* … *)`), every ladder routine as a PROGRAM, and every Add-On Instruction as a
FUNCTION_BLOCK in `lib/`. Alias tags, and rack points the logic names
directly, become tags with `alias:`. A rung that names an alias tag comes
in like any other: the tag takes its target's type, whether the target is
a tag, a member of a UDT or Add-On Instruction instance, an array element,
a bit (`Status.5`) or a rack point's bit (`Local:1:I.Data.3`). An alias the
import cannot type, such as one of a whole rack word, is refused by name.
An export with I/O modules also gives the project its hardware
configuration: `hardware.L5X`, holding the export's `<Modules>` section
unchanged, and `hardware: hardware.L5X` in the manifest, so the write-back
carries the same modules and the alias tags land on them. (An export whose
only module is the controller's own gets none: the writer generates that
module from the processor.) A preset the logic writes, such as
`MUL(DelaySec,1000,t.PRE)` ahead of the timer (common in Add-On
Instructions), comes in as a DINT of its own, `t_PRE`, seeded with the
export's `PRE`. The logic writes it, a read of `t.PRE` reads it, and the
call runs on it (`t:TON(PT := TO_TIME(t_PRE))`), so the timer times what
the logic computed rather than the export's value. An element at a literal
index (`Ts[3].PRE`) gets `Ts_3_PRE`; one at a computed index is refused by
name. A rung with no nautilus form is kept
as a comment and reported, never guessed at. `--comm-path` and `--host`
fill in how to reach the controller.

To check what came in, write it back and compare the logic with the
export:

```bash
naut logix write -o back.L5X line/MainProgram.ld
naut logix normalize --logic --check back.L5X Line.L5X
```

The write takes the controller, processor, revision, program, routine and
task (`continuous: true` for a continuous one) from the imported
`target: logix`, and each tag's initial value and description from the
tag file; the file claims the Logix Designer release of that revision, as
an export does (`SoftwareRevision="36.00"` for a v36 controller), and each
UDT carries its descriptions back. `--logic` compares programs, routines, rung text and comments,
and each tag's name, scope and type, and lists every difference; the two
files' envelopes always differ (the writer emits a whole project). The
import keeps two things a nautilus project would otherwise change. A
controller-scoped timer, counter or Add-On Instruction instance is a block
instance in nautilus, so it is declared in its program; the manifest lists
it under `target.logix.controller-scoped`, with its description, and the
write puts it back at controller scope, where a client reading
`AcceptTmr.ACC` finds it. And the manifest sets `all-tags: true`, so a
controller tag no imported routine names, such as one only an
EtherNet/IP client writes, is written back too; the tag file types each
tag (`type: DINT`, where `init: 0` alone would be a REAL). A tag with no
nautilus type (a `MESSAGE`, a multi-dimensional array), or with one the
target does not carry (a `STRING`, a `LINT`), stays in the tag file and is
reported, because the write cannot create it.
On the public yardstick, `examples/batch-skid/line/Line.L5X`, the
comparison finds no differences.

On a private corpus of 52 real production exports (a measurement we cannot
publish the inputs of), 98.3% of rungs import, 83% of ladder routines import
whole, and most complete routines write back
identically or equivalently. `COP` and `CPS` import when they copy like
for like (a whole array or structure, or a slice at constant indexes) and
write back as the instruction they were. An `MSG` of type CIP Data Table
Read or Write imports as a [`READ` or `WRITE`
block](https://github.com/joyautomation/nautilus/blob/main/docs/functions.md#read-and-write-another-device)
on a `channels:` entry for its peer, and writes back as the `MSG` it was.
Socket messages (CIP Generic) and the byte-buffer copies that build their
payloads stay refused.

## The loop

```bash
naut check                    # the target's rules, plus everything else
naut test                     # scenarios on the nautilus runtime, virtual time
naut logix deploy             # write, import and build through logixd
naut logix deploy --online    # ...and online-edit the running program
naut logix deploy --download --yes --program-mode
                              # ...or download: stops the controller
naut test --target logix      # the same scenarios on the controller, real time
naut logix serve --project .  # live values, set-value, online edit and download in VS Code
```

**`naut logix deploy`** writes the project as an L5X, has logixd import
and build it, and then:

- with no flag, stops there and says whether the running controller could
  take the change as an online edit or needs a download;
- with `--online`, replaces the routine's rungs in the running program and
  finalizes them, leaving the controller in its mode. It is refused when
  the tag set changed, because an online edit cannot create tags;
- with `--download --yes`, downloads the project. **That stops the
  controller.** The controller must be in Program mode already, or pass
  `--program-mode`. Tag values are kept (below); `--reset-values` starts
  every tag at the project's value instead.

Every path ends by uploading the controller's program again and comparing
its logic with what was sent (`naut logix drift --logic`). `--keep DIR`
keeps the generated L5X, the built ACD and the before/after uploads.

### What a download does with tag values

A download replaces the controller's whole project, its tag data with it.
So before the build, `naut logix deploy --download` uploads the running
controller and writes its current values into the project, and the
download carries them: setpoints, counts and recipe data the plant has
changed survive. Tag by tag:

| the tag | after the download |
|---|---|
| on both sides, same name and scope (controller, or the same program), same type and dimensions | **the controller's value** |
| a structure (UDT) or Add-On Instruction instance whose type is defined the same on both sides | **the controller's value**, every member |
| a `TIMER` or `COUNTER` | the controller's accumulator and status bits; **the preset is the project's**, because a literal `PT := T#30S` is code and a changed one must take effect |
| type, dimensions or the type's definition changed | the project's initial value, reported (`SpareSP: DINT → REAL`) |
| a constant in the project | the project's value |
| only in the project | the project's initial value, reported as new |
| only on the controller | gone (the download removes it), reported |

The deploy prints the outcome before the build (`tag values: 11 kept from
the controller, 1 reset to the project's value (SpareSP: DINT → REAL), 1
new (NewSP)`), and a build without `--download` prints what a download
would do. The values are the controller's at the upload, about half a
minute (the import and build) before the controller stops: a value the
program changes in that window is not carried. A changed `init:` in
`nautilus.yaml` does not override a kept value, since the controller's
value wins; use `--reset-values` (or change the tag's value live) when the
project's value must win. A ladder timer becoming an ST one changes its
type (`TIMER` → `FBD_TIMER`), so it starts fresh.

`naut logix mode [--yes run|program] <project-dir>` reads or changes the
controller's mode. `naut logix write program.ld` writes the L5X without
logixd, for inspection: for a project's task program it is what deploy
would send, and its flags override the manifest.

**`naut test --target logix`** runs the project's
[acceptance tests](https://github.com/joyautomation/nautilus/blob/main/docs/testing.md#against-a-logix-controller) against the controller over
EtherNet/IP: `given` writes go to the controller, `advance:` is wall time,
and `until`, `hold` and `always` are checked on every poll. A timing
assertion is only as sharp as one poll plus one scan, so give a timer's
`near` that much tolerance. REAL on the controller is 32-bit; compare with
`near`. A one-scan pulse can fall between polls, so latch it if a test
needs to see it.

## From the editor

With `naut logix serve --project .` running and `nautilus.runtimeUrl`
pointing at it, the loop never leaves VS Code. The server serves the
controller's live values (hover, the Live Values panel, the rung overlay)
and takes deploys from the editor through the same logixd agent the CLI
uses; the editor only asks it.

In a project whose `nautilus.yaml` has a `target: logix` section, a ladder
or ST program shows two commands, in the editor title bar and the command
palette:

- **nautilus: Push Online Edit to Logix Controller** (the cloud-upload
  button). The same as `naut logix deploy --online`: the routine's rungs
  are replaced and finalized in the running controller, which keeps its
  mode. It sends the open buffer, unsaved edits included. The first edit
  after the server starts opens an online session (upload, open, go
  online: about a minute); every edit after that is a rung import and a
  verification, about two seconds. **nautilus: Download Program to
  Controller** does the same thing on a Logix server.
- **nautilus: Download Project to Logix Controller** (in the title bar's
  `…` menu). The same as `naut logix deploy --download --yes`. A modal
  says first that the download **stops the controller**, that tag values
  are kept (the rule above) and that it leaves the controller in Program
  mode; choose **Download** if it is in Program mode already, or **Switch
  to Program Mode and Download**. The setting
  `nautilus.logix.resetValuesOnDownload` makes the download start every
  tag at the project's value, as `--reset-values` does, and the modal says
  so. The summary says how many values were kept and which tags were
  reset. The server re-reads `nautilus.yaml` for the
  download, so new tags go with it. The summary comes as soon as the
  download is verified; the server then opens the online session again
  (about a minute, in the output channel), so the next online edit is
  warm. An edit pushed meanwhile waits for that session and says so.
  A plain **Download** with the controller in Run is refused: the
  message says so, the SDK's own text is in the output channel, and the
  notification offers **Switch to Program Mode and Download**.

Both show the deploy's progress in a notification and in the **nautilus
Logix** output channel as it happens, and end with what happened: verified
or not, rungs replaced, the controller's mode, and the whole time you
waited (an online session opening included). A refusal is shown as the server words it.
A change an online edit cannot carry (new tags, a routine the controller
does not have) offers **Download Project…**.

Closing the tab, or losing the connection, does not stop a deploy that
has begun to write to the controller: the download or the online import,
and the verify after it, run to their end (only the server's 15-minute
limit stops them), and the journal records how it really ended. Before
that first write, closing the tab stops the deploy and nothing changes.
When nautilus itself stops (Ctrl-C on `naut logix deploy`, a killed
`naut logix serve`), `logixd` still runs a write it has started to its
end, but nothing verifies or journals it: check the controller as above.
A deploy that fails after the write began (that limit, an agent that
stopped answering, a verify that could not read the controller or found
it different) is answered **OUTCOME UNKNOWN**: the controller may hold
the new program. Check what it runs before anything else: `naut logix
deploy` without `--online` or `--download` compares it with the project
and changes nothing. The next online edit opens its session again and
reads the controller first. A deploy that verified but could not read the
controller's mode after the write is still a success, with a warning
(`modeWarning` on the `done` line, `warning` on the journal entry):
check whether the controller is in Run, above all after **Switch to
Program Mode and Download**.

Live values keep streaming through both: they are polled over
EtherNet/IP, separately from the deploy. If nothing answers at
`nautilus.runtimeUrl`, the commands offer to start `naut logix serve
--project` in a terminal. That terminal needs `NAUTILUS_LOGIXD_URL` (or
`target.logix.agent`) and `NAUTILUS_LOGIXD_TOKEN`, as the CLI does.

The server answers these commands on `POST /api/logix/deploy`:
`{"mode": "online" | "download", "source", "baseHash", "programMode",
"resetValues", "yes"}`; a download's `done` event carries `valuesKept`
and the `resets`. The answer is a stream of JSON lines (`progress` events, then
`done` or `error`; an `error` with `"outcome": "unknown"` failed after
the write to the controller began, and is journalled `unknown`). A download is refused unless the body says
`"yes": true`, and only one deploy runs at a time. The server needs
`--project`; without it, it serves live values only.

## What it does not do yet

- **One controller family, one revision verified, no real hardware yet.**
  Every behaviour test ran on an emulated 5580 at v38 on FactoryTalk Logix
  Echo. Other revisions and families are expected to work but are not yet
  in a test matrix. The project's controller module (`Local`) is Logix
  Designer's own for the ControlLogix 5580 (1756-L8xE), CompactLogix 5370
  (1769-L30ER to 1769-L36ERM) and CompactLogix 5380 (5069-L306ER to
  5069-L3100ERM) processors, and a project written for a 5069-L320ER or a
  1769-L33ER imports and builds in Logix Designer v38. For any other
  processor (a 5370 L1 or L2, a 5480, a safety controller, a 5570) it is
  the 1756-L85E's with only the catalog number changed; `naut logix write`
  and `naut logix deploy` say so, and Logix Designer will likely refuse
  the project, unless the project has a hardware configuration
  (`target.logix.hardware`), whose own `Local` module is used instead.
- **One task, one program.** Multi-task projects, FBD and SFC are not in
  the subset.
- **Not everything IEC 61131-3 has.** No `STRING`, unsigned, bit-string,
  `LINT` or `LTIME` types; no `TP`, `CTD`, `CTUD` or edge-trigger
  instances; no `MIN`, `MAX`, `LIMIT`, `SEL`, `MUX` or `ATAN2` in ST; no
  user FUNCTIONs; no arrays of your own function blocks (arrays of
  timers and counters are fine); in ST, an Add-On Instruction's input is
  a tag or a number, not an expression. A conversion the controller would
  compute differently, and an integer `/` whose quotient meets a REAL,
  are refused. Each is a [check-time
  diagnostic](#rejected-at-check-time-by-name) that names the rule.
- **The numbers are the controller's.** A REAL is 32 bits on the
  controller and 64 in nautilus, so compare REALs with `near`. A REAL out
  of range for the integer it is stored into wraps silently on the
  controller, where nautilus faults the scan. An integer store wraps at its
  declared width on both.
- **The hardware configuration comes from Logix Designer.** The writer
  carries an exported `<Modules>` section; it does not create a module from
  a description (catalog number and slot) yet, so a new module is added in
  Logix Designer and exported. With a hardware configuration, the
  controller's own module is the configuration's, not the writer's.
- **A download keeps values only for tags it can match.** A renamed or
  retyped tag starts at the project's value, and the values are the ones
  uploaded about half a minute before the controller stops. There is no
  warm download: it still stops the controller. An online edit keeps every
  value and does not stop anything, but cannot add tags.
- **New tags need a serve restart.** `naut logix serve` reads the
  controller's tags when it starts, so a tag a download adds shows in
  live values after a restart.
- **Import is not complete.** A rung with no nautilus form stays a
  comment and is reported; socket messages (CIP Generic `MSG`) and the
  byte-buffer copies that feed them stay refused.
- **No runtime parity promise.** The subset is the set of constructs whose
  behaviour on both runtimes has been checked by running the same
  scenarios on each. Anything outside it is refused, not approximated.

### Licensing

Read the Studio 5000 SDK licence for yourself. As we read it, it does not
prohibit headless or CI use, but each concurrent user of a logixd agent
needs their own SDK activation, and the SDK may not be offered as a hosted
service: run logixd on your own licensed machine for your own team, and
**do not host a logixd agent for other people.**

The commercial licence terms that bear on building a product on the SDK
(the automation-interface and pooling clauses) are not settled.
