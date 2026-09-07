---
title: Ladder Diagram (LD)
description: Rung text that compiles through FBD to the same IR as every other nautilus program, with a full graphical ladder editor over it in VS Code.
sidebar:
  order: 2
---

IEC 61131-3 defines ladder graphically. nautilus defines it as text: a `.ld`
file holds rungs written left to right along the power rail, in a grammar
with the standard's semantics and no vendor dialect. The VS Code extension
projects that text into a ladder editor where every gesture writes back to
the same file.

## When to use it

Ladder fits boolean logic that gets reviewed by asking which contacts passed
power: interlocks, permissives, latches, motor seal-ins, annunciators and
alarm horns. It is the language most control engineers read fastest, and one
rung is one line of `git diff`.

Loops and signal conditioning read better as [Function Block
Diagram](/languages/function-block/). Math, arrays, and plant physics belong
in [Structured Text](/languages/structured-text/). Mix them per task against
one tag store.

## A rung, in text

`interlocks.ld` from the heated-tank example, the annunciator running as its
own 200 ms task:

```iecld
PROGRAM Interlocks
VAR_EXTERNAL
    TempLowAlm : BOOL;
    TempC      : REAL;
    HornAck    : BOOL;
    Horn       : BOOL;
    HiTempAlm  : BOOL;
END_VAR
VAR
    HiSecs : TIME; (* how long we've been hot — captured via ET => *)
END_VAR
LD
  RUNG hitemp (* comparison as a contact, 5 s on-delay; ET captured with the standard's => output binding *)
    GT(TempC, 90.0) t2:TON(PT := T#5S, ET => HiSecs) ( HiTempAlm )

  // Comment runs like this render as notes in the ladder diagram —
  // dblclick to edit them there.
  RUNG horn (* either alarm sounds the horn until the operator acks *)
    [ TempLowAlm | HiTempAlm ] /HornAck ( Horn )

  RUNG ackclear (* the ack releases itself once both alarms clear *)
    /TempLowAlm /HiTempAlm ( R HornAck )
END_LD
END_PROGRAM
```

Every element the grammar accepts:

| Element | Text | What it does |
| --- | --- | --- |
| NO contact | `Tag` | passes power when `Tag` is TRUE |
| NC contact | `/Tag` | passes power when `Tag` is FALSE |
| Parallel branch | `[ a \| b ]` | OR of its legs; each leg is a series, and branches nest |
| Function contact | `GT(TempC, 90.0)` | passes power when the call returns TRUE |
| Block in the rung | `t2:TON(PT := T#5S)` | power drives its power-in pin, and continues from its power-out pin |
| Output capture | `ET => HiSecs` | binds a non-BOOL output pin to a variable, inside the block's parentheses |
| Output coil | `( Tag )` | `Tag :=` the rung condition, every scan |
| Set coil | `( S Tag )` | latch: `Tag := Tag OR condition` |
| Reset coil | `( R Tag )` | unlatch: `Tag := Tag AND NOT condition` |

Contacts and coils take the same accessor references FBD takes, so array
elements and struct members address directly: `Levels[2]`, `PIT_001.VALUE`.
A rung header carries a `(* … *)` comment; full-line `//` comments inside
the block are diagram notes.

## How a rung evaluates

Series elements AND together and branch legs OR, so
`[ Start | Run ] /Stop ( Run )` compiles to
`Run := AND(OR(Start, Run), NOT Stop)`. That is the seal-in: Start energizes
Run, Run's own contact holds it in, Stop drops it out.

Coils sit at the right end of a rung, and a rung needs at least one. Several
coils share one condition, evaluated once and fanned out. A rung with only a
coil is driven by the rail, so `( AlwaysOn )` assigns TRUE. Rungs evaluate
top to bottom within a scan, so a coil written on one rung reads back as a
contact on the next rung in the same scan.

Power is boolean, so anything gating it has to return BOOL. The six
comparisons do. Numeric functions do not, and `ADD(TempC, 0.0)` as a contact
is a compile error rather than a pass-through; numeric work goes inside a
comparison's arguments, as `GE(ADD(Base, Bias), Limit)`.

When a block sits in a rung, power drives one input pin and leaves from one
output pin; every other pin is named in the parentheses. `TON`, `TOF` and
`TP` use `IN` and `Q`, `CTU` uses `CU`, `CTD` uses `CD`, `R_TRIG` and
`F_TRIG` use `CLK`, `SR` uses `S1`/`Q1`, `RS` uses `S`/`Q1`. Passing the
power pin yourself (`t1:TON(IN := x)`) is an error, because power owns it.

Coils assign BOOL only. To store a timer's elapsed time or a counter's
value, use the standard's output binding at the call site, `ET => HiSecs`
above. Any instance output is also readable as `inst.Pin` anywhere,
including as a contact: `GE(t2.ET, T#2S)`.

## Types, functions, and blocks

LD transpiles in one hop to the FBD netlist, which transpiles to ST. Ladder
inherits the whole chain rather than reimplementing it: the built-in
operator, function and function-block vocabulary, user `FUNCTION`s and
`FUNCTION_BLOCK`s from library `.st` files, arrays and structs through
accessor references, and typed diagnostics mapped back to the rung.

A user block drops into a rung the way a TON does, with power on `IN` and
continuing from `Q`, so a block meant for rung use declares those two pins.
Any block is readable from a rung through `inst.Pin` whatever its pins are
called. Instance state persists across scans, and an online edit carries it
by name and type. See [Function blocks, libraries, and
tasks](/guides/blocks-and-tasks/); library files are ST today.

## In the editor

Right-click a `.ld` file and choose **Open With → Ladder Diagram** to use
the diagram as the editor, or run **nautilus: Open Ladder Diagram Preview**
for text on the left and rungs on the right. Layout is canonical, computed
from the source, so no coordinates land in your diffs. Editing gestures
resolve through the Go compiler into minimal text edits:

- A palette of NO contact, NC contact, function contact, TON, CTU, parallel
  branch, and output/set/reset coil. Click to append at the selection, or
  drag onto a rung.
- Double-click a contact or coil to retag it, a function contact to rewrite
  the call, a block to edit its arguments, the rung name to rename it, and
  the `(* … *)` slot to write a rung comment.
- `N` flips a contact between NO and NC, `M` cycles a coil through output,
  set and reset, `B` wraps the selection in a parallel branch, `Del`
  deletes, and Ctrl+X/C/V cut, copy and paste. A pasted block instance gets
  a fresh name.
- Drag an element to reorder it or move it to another rung. A contact
  dropped in the coil zone is refused, and so is a coil dropped mid-series.
- `+ rung` adds a rung, `//` adds a comment note, and the variables panel
  declares or removes a declaration without leaving the diagram.

With a controller reachable, live values paint power flow: contacts and
coils show their state, the wires show whether power reaches that far, and
comparisons evaluate locally. An in-rung block reads its real output pin
from the controller. Anything unresolvable renders neutral.

**nautilus: Diff Ladder Diagram (vs git HEAD)** and **(vs Controller)**
overlay the committed or running rungs on the working ones, marking added,
changed and removed elements in cyan, amber and red. Green already means
power here.

## Tooling

```sh
nautilus check                      # compile every .st/.fbd/.ld/.sfc; errors land on the rung
nautilus test                       # acceptance tests, on the virtual clock
nautilus ld graph interlocks.ld     # the ladder render model as JSON
nautilus ld edit                    # apply one structural edit op, from JSON on stdin
```

`nautilus check` composes the line maps back through both hops, so a type
error in the ST a rung produced reports at that rung's `RUNG` line. The
language server does the same live.

Online edits treat `.ld` as the program source. Download, diff against the
controller, and `nautilus pull` all move the ladder text itself, so the
program a controller reports compares 1:1 with the file in git. See [Online
edits](/guides/online-edits/).

A rung change reviews as a rung:

```diff
   RUNG horn (* either alarm sounds the horn until the operator acks *)
-    [ TempLowAlm | HiTempAlm ] /HornAck ( Horn )
+    [ TempLowAlm | HiTempAlm | Overfill ] /HornAck ( Horn )
```

`nautilus new my-plant --template minimal --language ld` scaffolds a project
whose program is ladder.

## Not supported

- **Jumps and labels.** No `JMP`, `JMPC`, `LBL`, `RET`. Rungs run top to
  bottom, every scan.
- **Master control relay zones.** No `MCR` or zone bracketing.
- **Negated coils.** `( /Tag )` is rejected; invert with NC contacts or use
  a reset coil.
- **Negated function contacts.** `/GT(a, b)` is rejected; write the opposite
  comparison.
- **Coils inside a branch.** Coils sit at the rung's right end. Several
  coils on one rung drive more than one output.
- **A rung with no coil.** Rejected even when the rung holds a function
  block. Give the block's `Q` a coil, or read `inst.Q` from another rung.
- **Edge contact modifiers.** No `|P|` / `|N|` form. One-shots are
  `e1:R_TRIG()` and `e1:F_TRIG()` in the rung.
- **Vendor instruction sets.** The vocabulary is the IEC built-ins in the
  [language reference](/reference/functions/), and nothing else.
- **Authoring function blocks in ladder.** Library files are `.st` today.

## See also

- [Language reference](/reference/functions/) — power flow, power pins, and
  every built-in.
- [Function blocks, libraries, and tasks](/guides/blocks-and-tasks/)
- [Online edits](/guides/online-edits/)
- [Testing](/reference/testing/)
- [Structured Text](/languages/structured-text/) ·
  [Function Block Diagram](/languages/function-block/) ·
  [Sequential Function Chart](/languages/sfc/)
