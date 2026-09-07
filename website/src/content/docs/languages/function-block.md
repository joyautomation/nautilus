---
title: Function Block Diagram (FBD)
description: "FBD in nautilus is a git-diffable text netlist with a full diagram editor over it: blocks, wires, pins, negation circles, and seal-in feedback."
sidebar:
  order: 3
---

An FBD program is a text file. `.fbd` source is a netlist of named wires
driven by blocks, coils that write variables, and function-block instances,
all inside an otherwise ordinary ST POU. The VS Code extension projects that
netlist into the diagram a controls engineer expects, and an edit made on the
diagram is written back into that same file as text. Values flow left to right
through blocks, which is what makes FBD fit continuous control: PI loops,
filters and scaling, selection and limiting, signal conditioning in stages.

## When to use it

Reach for FBD when the logic is mostly analog. A temperature loop with an
anti-windup clamp, a rate-of-change calculation, a rolling average, a
setpoint selector: these read as a chain of blocks, and the diagram shows
the chain.

Interlock logic that is almost entirely contacts and coils reads better as
[ladder](/languages/ladder/). A step sequence with holds and aborts belongs
in [SFC](/languages/sfc/). Anything needing branching, loops, or `CASE` is
[Structured Text](/languages/structured-text/) work; the netlist has no
control flow. Tasks can mix languages, one program file each, so a project
usually uses more than one.

## A netlist, line by line

This is `examples/heated-tank-nogo/program.fbd`, complete:

```iecfbd
PROGRAM Main
(* Heated surge tank control, FBD flavor: the pump hysteresis is a seal-in
   latch, the temperature PI feeds its integral back through a retained
   variable, and a TON delays the low-temperature alarm.
   Open the diagram: right-click -> "Open With -> FBD Diagram". *)
VAR_EXTERNAL
    LevelPct       : REAL;
    TempC          : REAL;
    ScanDtS        : REAL;
    TempSP         : REAL;
    TempSPEco      : REAL;
    EcoMode        : BOOL;
    Kp             : REAL;
    Ki             : REAL;
    PumpStartLevel : REAL;
    PumpStopLevel  : REAL;
    PumpRun        : BOOL;
    Heater         : REAL;
    TempLowAlm     : BOOL;
END_VAR
VAR
    integral : REAL;
END_VAR
FBD
  // P-101 pump: level hysteresis as a seal-in latch — start when low,
  // seal through PumpRun, drop out when high.
  low  = LE(LevelPct, PumpStartLevel)
  high = GE(LevelPct, PumpStopLevel)
  PumpRun := AND(OR(low, PumpRun), NOT high)

  // Eco mode selects the working setpoint (SEL: FALSE takes IN0).
  spNow = SEL(EcoMode, TempSP, TempSPEco)

  // TIC-101: temperature PI with anti-windup clamp; integral is retained
  // state fed back from its own coil.
  e = SUB(spNow, TempC)
  integral := LIMIT(0.0, ADD(integral, MUL(Ki, e, ScanDtS)), 30.0)
  Heater := LIMIT(0.0, ADD(MUL(Kp, e), integral), 100.0)

  // TAL-101: low-temperature alarm with a 10 s on-delay.
  cold = LT(TempC, 62.0)
  a1 : TON(IN := cold, PT := T#10S)
  TempLowAlm := a1.Q
END_FBD
END_PROGRAM
```

Everything before `FBD` and after `END_FBD` is plain ST: the POU header and
its `VAR` sections, compiled by the same front end as a `.st` file. The body
accepts these forms.

| Element | Written as | What it is |
| --- | --- | --- |
| Wire | `low = LE(LevelPct, PumpStartLevel)` | a named block output, defined once |
| Coil | `Heater := LIMIT(...)` | writes a variable; the target may be an array element or struct member, as in `TempHist[1] := TempC` |
| FB instance | `a1 : TON(IN := cold, PT := T#10S)` | declares the instance and calls it in one statement; `a1 : TON` alone declares it, `a1(IN := ...)` calls it later |
| Pin read | `a1.Q` | an output pin of an FB instance |
| Output binding | `ET => Elapsed` | IEC's formal-call output form, for capturing a non-BOOL pin such as `ET` or `CV` into a variable |
| Negation | `NOT high` | inline pin negation, drawn as the IEC circle on the pin |
| Constant | `62.0`, `T#10S`, `TRUE`, `'ok'` | a literal input chip |
| Accessor | `TempHist[2]`, `M.Speed` | array element or struct member, as an input or a coil target |

Full-line `//` comments inside the body render as notes on the diagram.
Statements may end with `;` or just a newline.

## How a diagram evaluates

Compilation transpiles the netlist to equivalent ST and runs it through the
ST front end, so FBD inherits the whole type system, the standard library,
and the diagnostics. Errors map back to the exact `.fbd` line.

Wires are pure combinational expressions. A wire is inlined at every place
it is read, so fan-out duplicates an expression rather than creating shared
state, and wires must be acyclic: a wire that reads itself, directly or
through another wire, is a compile error ("combinational loop through wire").

Variables carry state. That is where feedback lives. `PumpRun := AND(OR(low,
PumpRun), NOT high)` reads the same variable the coil writes, which is a
seal-in latch and evaluates exactly as a PLC evaluates one. The same
mechanism retains the PI `integral` across scans, and gives `reports.fbd` its
four-element shift register: a variable read early in the body carries the
value its coil wrote on the previous scan.

Statements evaluate in source order, with one adjustment: a function-block
call is emitted ahead of any statement that reads its output pins, so `a1.Q`
reads this scan's result no matter where `a1` sits in the file. Ties keep
source order. There is no other reordering and no way to override it.

## Types, functions, and blocks

FBD uses the same vocabulary as Structured Text. `AND`, `OR`, `XOR`, `ADD`
and `MUL` take two or more inputs (the `+` pin in the editor adds one);
`SUB`, `DIV`, `MOD` and the comparisons take exactly two; `MOVE` is a
one-input pass-through for wiring. Standard functions such as `LIMIT`, `SEL`,
`SQRT` and `CONCAT` are written as blocks with positional inputs, and
standard function blocks such as `TON`, `CTU` and `R_TRIG` are instantiated
with named pins. User-defined `TYPE` structs work as inputs and coil targets.
The whole list is in the [language reference](/reference/functions/).

A `FUNCTION_BLOCK` you write yourself instantiates the same way a `TON` does:

```iecfbd
  r1 : RateOfChange(IN := TempC, DT := RepDtS)
  TempRate := r1.OUT
```

`RateOfChange` is authored in an ST library file (`blocks.st`) and composed
ahead of the program. A `FUNCTION_BLOCK` can carry an FBD body as well: a
`.fbd` file with no `PROGRAM` is a project library, and its blocks
instantiate from any language. Write a block once, call it from whichever
language fits the logic. See [function blocks, libraries, and
tasks](/guides/blocks-and-tasks/).

## In the editor

Right-click a `.fbd` file and choose **Open With → FBD Diagram** to use the
diagram as the editor itself, or run **nautilus: Open FBD Diagram Preview**
to open it beside the text, which then updates as you type.

Structural gestures are resolved by the Go compiler into minimal text edits
against the file: double-click a constant to retype it or a block to rename
it, click an input pin to toggle `NOT`, drag an output onto a pin to rewire,
delete a selected node, and insert statements from the **+ add** palette
(block-to-wire, coil, timer, counter, comment, bare reference chips, and
variable declarations). Layout is computed from topology by default. Drag a
node to pin it and the position is stored in a `(* @layout *)` comment that
the compiler skips; **auto layout** clears every pin.

The **vars** button opens a panel listing every header declaration, wired or
not, with its section, its live value, an `unused` marker when nothing in the
logic references it, and an amber **no tag** badge on a `VAR_EXTERNAL` with
no matching tag on the controller. A read of a tag that was never written
faults the scan, so clear that badge before you download.

With a controller reachable, live values paint onto the diagram: value pills
on variable chips and on FB output pins, alongside the inline values in the
text editor.

**nautilus: Diff FBD Diagram (vs git HEAD)** overlays the committed and
working-tree diagrams, coloring added, removed and changed blocks and wires.
`(vs Controller)` compares against the program a live controller is running.

## Tooling

```sh
nautilus check              # compile .fbd (and .st/.ld/.sfc); the CI gate
nautilus test               # acceptance tests, virtual clock
nautilus fbd graph f.fbd    # the diagram render model as JSON
nautilus fbd edit           # apply one structural edit op; stdin/stdout JSON
```

A controller running an FBD program serves and accepts the `.fbd` text
itself, so download, text diff, `nautilus pull` and the sync status bar work
as they do for `.st`. A warm swap migrates retained state by name and type,
so a PI integral or a running `TON` keeps its value through the edit; see
[online edits](/guides/online-edits/). An edit routes to a task by `PROGRAM` name,
so `reports.fbd` downloads without disturbing `Main`.

Because the source is text, a wiring change reviews as a diff:

```diff
-  cold = LT(TempC, 62.0)
+  cold = LT(TempC, 55.0)
```

## Not supported

- Authoring a `FUNCTION` in FBD. Functions are ST; blocks may be FBD or
  ladder.
- `EN`/`ENO` pins. Blocks always evaluate.
- Execution-order overrides. Order is source order plus the FB-before-reader
  rule described above.
- Control flow: no `IF`, `CASE`, or loops in the netlist.
- Inline arithmetic in an array index. Compute the index on a named wire
  first, then use `Levels[i]`.
- Defining the same wire twice, or a wire that feeds itself. Both are
  compile errors.

## See also

- [Language reference](/reference/functions/) — evaluation semantics and
  every built-in operator, function, and function block.
- [Function blocks, libraries, and tasks](/guides/blocks-and-tasks/)
- [The tag model](/guides/tag-model/)
- [Online edits](/guides/online-edits/)
- [Testing](/reference/testing/)
- [Structured Text](/languages/structured-text/),
  [Ladder](/languages/ladder/),
  [Sequential Function Chart](/languages/sfc/)
