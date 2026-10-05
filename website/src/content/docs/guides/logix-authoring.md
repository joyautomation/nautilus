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

The manifest's tags become controller-scope Logix tags, with their initial
values and descriptions. A program's `VAR` become program tags. The task
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
| `{ y := expr }` | `MOVE`, `ADD`, `SUB`, `MUL`, `DIV`, `MOD`, `NEG`, `ABS`, `SQR`, `XPY`, or `CPT` |
| `t:TON` `t:TOF`, `c:CTU` | `TON` `TOF` `CTU` on `TIMER` / `COUNTER` tags; `t.Q`→`t.DN`, `t.ET`→`t.ACC` |
| `t:TONR` (with `dialect: logix`) | a `TON` with a `RES(t)` rung ahead of it |
| `Word.3` | bit 3 of the word, as Logix spells it |
| `Timers : ARRAY [0..3] OF TON` | one `TIMER[4]` tag |
| BOOL, SINT, INT, DINT, REAL, LREAL | the same types |
| `TIME` | a DINT of milliseconds, where it feeds a preset |
| STRUCT types in a library | UDTs |
| user FUNCTION_BLOCKs (ladder or ST) | Add-On Instructions |
| ST: assignments, IF, CASE, FOR, WHILE, REPEAT, EXIT, operators, maths | Logix ST, close to as written |

The language features in that table are documented with the rest of the
language: [the assignment element, bit access, arrays of instances and
dialects](https://github.com/joyautomation/nautilus/blob/main/docs/functions.md).

### Rejected at check time, by name

What the target cannot express is a `naut check` diagnostic that names the
construct and the alternative, before anything is written, never a
surprise at download:

```text
MainProgram.ld:11:1: logix target: pulse: type TP is not in the Logix v1 subset;
  its IEC load/reset semantics differ from the Logix instruction; ... [logix/type]
MainProgram.ld:18:1: logix target: pulse:TP: not in the Logix v1 subset; ...
  the v1 blocks are TON, TOF and CTU (and TONR with dialect: logix) [logix/fb]
```

A project with a `target: logix` section gets these from `naut check`
without asking; `naut check --target logix` asks for them on any project.
(The editor's live diagnostics do not run them yet.) Each carries a stable
rule ID:

| Rule | What it enforces |
|---|---|
| `logix/type` | the types in the table above; an AOI takes structures and arrays only as `VAR_IN_OUT` |
| `logix/fb` | no `TP`, `CTD`, `CTUD`, `R_TRIG`/`F_TRIG` instances (IEC and Logix semantics differ) |
| `logix/fb-pin` | timers bind `PT` (TONR also `Reset`), counters `PV` and `R`; the rung's power drives `IN` |
| `logix/function-block` | user FUNCTIONs have no Logix form (inline them); an AOI cannot reach a controller tag |
| `logix/var-section` | a program takes no parameters: only `VAR` and `VAR_EXTERNAL` |
| `logix/time` | `TIME` only where it feeds a preset |
| `logix/preset` | a preset is a literal or a declared variable |
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
| `TONR` (`IN`, `PT`, `Reset` → `Q`, `ET`) | a TON whose `Reset` clears it while TRUE; `t:TONR(PT := T#1S, Reset := t.Q)` is a free-running pulse | a `TON` with a `RES(t)` rung ahead of the timer's rung |

More will follow (first scan, wall clock and scan time, `COP`). The
default dialect is `nautilus`, which adds nothing.

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

The writer does not create I/O modules, so an alias to a rack point only
builds against a controller project that already has the module. See
[What it does not do yet](#what-it-does-not-do-yet).

## Bringing an existing program in

```bash
naut logix import --project ./line Line.L5X
```

writes a nautilus project from a full L5X export: the manifest (with
`target: logix` filled from the controller, and `dialect: logix` when the
export uses its idioms), the tags with their descriptions, UDTs as IEC
types, every ladder routine as a PROGRAM, and every Add-On Instruction as a
FUNCTION_BLOCK in `lib/`. Alias tags, and rack points the logic names
directly, become tags with `alias:`. A rung with no nautilus form is kept
as a comment and reported, never guessed at. `--comm-path` and `--host`
fill in how to reach the controller.

On a corpus of 52 real production exports, 97.2% of rungs import, and most
complete routines write back identically or equivalently. The largest
remaining gaps are `MSG` instructions, status flags (`S:FS`), `GSV`, and
`COP`.

## The loop

```bash
naut check                    # the target's rules, plus everything else
naut test                     # scenarios on the nautilus runtime, virtual time
naut logix deploy             # write, import and build through logixd
naut logix deploy --online    # ...and online-edit the running program
naut logix deploy --download --yes --program-mode
                              # ...or download: stops the controller
naut test --target logix      # the same scenarios on the controller, real time
naut logix serve --project .  # live values, set-value and Download in VS Code
```

**`naut logix deploy`** writes the project as an L5X, has logixd import
and build it, and then:

- with no flag, stops there and says whether the running controller could
  take the change as an online edit or needs a download;
- with `--online`, replaces the routine's rungs in the running program and
  finalizes them, leaving the controller in its mode. It is refused when
  the tag set changed, because an online edit cannot create tags;
- with `--download --yes`, downloads the project. **That stops the
  controller and resets its tags to the project's values.** The controller
  must be in Program mode already, or pass `--program-mode`.

Every path ends by uploading the controller's program again and comparing
its logic with what was sent (`naut logix drift --logic`). `--keep DIR`
keeps the generated L5X, the built ACD and the before/after uploads.

`naut logix mode [--yes run|program] <project-dir>` reads or changes the
controller's mode. `naut logix write program.ld` writes an L5X without a
project, for inspection.

**`naut test --target logix`** runs the project's
[acceptance tests](https://github.com/joyautomation/nautilus/blob/main/docs/testing.md#against-a-logix-controller) against the controller over
EtherNet/IP: `given` writes go to the controller, `advance:` is wall time,
and `until`, `hold` and `always` are checked on every poll. A timing
assertion is only as sharp as one poll plus one scan, so give a timer's
`near` that much tolerance. REAL on the controller is 32-bit; compare with
`near`. A one-scan pulse can fall between polls, so latch it if a test
needs to see it.

## What it does not do yet

- **One controller family, one revision verified.** Every behaviour test
  ran on a 5580 at v38 on FactoryTalk Logix Echo. Other revisions and
  families are expected to work but are not yet in a test matrix.
- **One task, one program.** Multi-task projects are not in the subset.
- **No hardware configuration.** The writer emits no I/O modules, so
  aliases to rack points need a controller project that already has them.
- **A download resets tag values.** Values the controller holds are not
  carried across a download, and there is no warm download. An online edit
  keeps them, but cannot add tags.
- **No runtime parity promise.** The subset is the set of constructs whose
  behaviour on both runtimes has been checked by running the same
  scenarios on each. Anything outside it is refused, not approximated.

### Licensing

Read the Studio 5000 SDK licence for yourself. As we read it, it does not
prohibit headless or CI use, but each concurrent user of a logixd agent
needs their own SDK activation, and the SDK may not be offered as a hosted
service: run logixd on your own licensed machine for your own team, and
**do not host a logixd agent for other people.**
