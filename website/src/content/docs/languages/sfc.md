---
title: Sequential Function Chart (SFC)
description: Steps, transitions, and actions in the IEC standard's own textual form — a git-diffable .sfc file that opens as a chart editor in VS Code.
sidebar:
  order: 4
---

An SFC program is a `.sfc` file: an ST POU whose body is an `SFC … END_SFC`
block written with IEC 61131-3's own keywords, `INITIAL_STEP`, `STEP`,
`TRANSITION`, and `ACTION`. The text is the program, and the VS Code
extension projects it into the classic chart view where every gesture is a
structural edit to that text.

## When to use it

Reach for SFC when the process has phases: batch sequences, startup and
shutdown, state machines that would otherwise be a pile of latches. A step is
a state, a transition is the condition that ends it, and actions drive
outputs while a step is active, so the chart reads as the sequence of
operations the process engineer wrote down. Continuous control belongs in
[FBD](/languages/function-block/), interlocks in
[ladder](/languages/ladder/), and algorithms in
[ST](/languages/structured-text/).

## A chart, in text

`examples/tank-batch-sfc/program.sfc`, trimmed of commentary. It fills a
tank, heats and mixes concurrently, drains, and counts the batch, with an
abort path off `Fill`.

```iecsfc
PROGRAM TankBatch
VAR_EXTERNAL
    Level : REAL; TempC : REAL; Start : BOOL; Abort : BOOL;
    FillSP : REAL; EmptySP : REAL; HeatSP : REAL;
    FillValve : BOOL; DrainValve : BOOL; Heater : BOOL; Mixer : BOOL;
    RunLamp : BOOL; AbortLamp : BOOL; BatchCount : INT;
END_VAR
VAR
    mixT : TON;
END_VAR
SFC

  INITIAL_STEP Idle:
    N  RunLamp;
    R  AbortLamp;              (* clear the latch on return to Idle *)
  END_STEP

  STEP Fill:
    N  RunLamp;
    N  FillValve;
  END_STEP

  STEP Heat:
    N  RunLamp;
    N  HeatCtrl;               (* ACTION block, below *)
  END_STEP

  STEP Mix:
    N  RunLamp;
    N  Stir;
  END_STEP

  STEP Drain:
    N   RunLamp;
    N   DrainValve;
    P1  CountBatch;            (* body runs once, on activation *)
  END_STEP

  STEP Aborted:
    S  AbortLamp;              (* latched until Idle resets it *)
  END_STEP

  TRANSITION t_start FROM Idle TO Fill := Start AND NOT Abort;
  END_TRANSITION

  (* alternative divergence: abort is declared first, so it has priority *)
  TRANSITION t_abort FROM Fill TO Aborted := Abort;
  END_TRANSITION
  TRANSITION t_full FROM Fill TO (Heat, Mix) := Level >= FillSP;
  END_TRANSITION

  (* simultaneous convergence: both sources must be active *)
  TRANSITION t_done FROM (Heat, Mix) TO Drain := (TempC >= HeatSP) AND mixT.Q;
  END_TRANSITION

  TRANSITION t_empty  FROM Drain   TO Idle := Level <= EmptySP;
  END_TRANSITION
  TRANSITION t_resume FROM Aborted TO Idle := NOT Abort;
  END_TRANSITION

  ACTION HeatCtrl:
    Heater := Heat.X AND (TempC < HeatSP);
  END_ACTION

  ACTION Stir:
    Mixer := Mix.X;
    mixT(IN := Mix.X, PT := T#3S);
  END_ACTION

  ACTION CountBatch:
    BatchCount := BatchCount + 1;
  END_ACTION

END_SFC
END_PROGRAM
```

| Element | Form | Notes |
| --- | --- | --- |
| Initial step | `INITIAL_STEP Idle: … END_STEP` | Exactly one per chart; active on the first scan |
| Step | `STEP Fill: … END_STEP` | Owns a retained BOOL, `Fill.X`, and a `Fill.T` elapsed time |
| Transition | `TRANSITION [name] FROM a TO b := <ST expr>; END_TRANSITION` | `FROM`/`TO` take one step or a parenthesised list; the name is optional |
| Action block | `ACTION Stir: <ST statements> END_ACTION` | The body is Structured Text |
| Association | `N FillValve;` | A qualifier plus either an `ACTION` name or a declared BOOL variable |

## How a chart executes

Each step owns a retained `BOOL` slot, and the initial step's is declared
`:= TRUE`, so a cold start finds exactly one step active. Every scan runs one
fixed-order pass: evaluate each transition against the pre-scan activity
snapshot, resolve which ones fire, clear the sources, set the targets, update
step timers, compute actions. Clears run before sets, so a self-loop
`FROM S TO S` leaves `S` active. Every transition reads the same snapshot, so
a token advances at most one step per scan, though several transitions can
fire in one scan when several tokens are live.

A transition is *enabled* when all of its `FROM` steps are active and its
condition is true. That one rule covers both branch forms:

- **Alternative divergence** is two or more transitions sharing a source
  step. Priority is declaration order, first highest, and a lower branch is
  suppressed only when a higher one sharing a source is itself enabled, so a
  convergence waiting on an inactive source cannot deadlock the branch below.
- **Simultaneous divergence** is `TO (Heat, Mix)`: one transition sets both
  targets and the token splits. **Simultaneous convergence** is
  `FROM (Heat, Mix)`, enabled only when both sources are active.

Qualifiers that compile today are `N`, `S`, `R`, `P1`, `P`, and `P0`. `N` is
active exactly while the step is. `S` latches the target on the step's rising
edge and `R` clears it, which is the abort/reset pattern above. `P1` and `P`
fire once on the rising edge, `P0` once on the falling edge. Associations
targeting the same BOOL variable are OR-combined, so `RunLamp` drops to
`FALSE` once no step drives it.

Action bodies are ST, and only ST. A body driven by a level qualifier runs
one extra scan on the falling edge of its active signal, so a body written
against step activity shuts its outputs down: on that final scan
`Mixer := Mix.X` writes `FALSE` and `mixT(IN := Mix.X, …)` resets, clean for
re-entry. A `P1` body has no final scan.

`Step.T` compiles to a hidden `TON`, and only for steps whose `.T` is read.
Both `.X` and `.T` are legal in conditions and action bodies.

## Structural checks

`nautilus check` runs the chart-shape checks before the ST hop;
`nautilus sfc check <file>` runs them alone.

Errors: duplicate step, action, or transition names; no `INITIAL_STEP`, or
more than one; a `FROM`/`TO` naming a step that does not exist; an empty
condition; a non-initial step that no transition targets (unreachable); an
association naming neither an `ACTION` block nor a declared variable; a
`.X`/`.T` reference to an unknown step; an unsupported qualifier.

Warnings: a dead-end step that no transition sources, which a terminal step
may be on purpose; a simultaneous convergence whose sources are not reachable from a
common simultaneous divergence; an alternative priority group whose
shared-source overlap is not transitive.

The chart then transpiles to ST and compiles like any other program, the line
map putting a type error in a condition on its `TRANSITION` line and one in a
body on its `ACTION` line.

## In the editor

Right-click a `.sfc` file and pick **Open With → SFC Diagram**, or run
**nautilus: Open SFC Diagram Preview** beside the text. Steps draw as boxes,
double-bordered for the initial step, with their associations tabled
alongside. Transitions draw as bars across the flow line, single for a normal
or alternative transition and double for a simultaneous one; a transition
back up the chart becomes a compact `↩` jump glyph. Layout follows the
chart's topology; drag a step to pin it, and "auto layout" clears every pin.

Every gesture is a structural op resolved in Go into minimal text edits: add
a step or transition, branch an alternative or simultaneous path off one,
drag a step's connect handle onto another step to wire a transition, edit a
condition, step name, association, or `ACTION` body in place. A transition
whose `FROM` or `TO` no longer resolves stays on the canvas as a red chip
with a retarget popover, and diagnostics show as you type without blocking a
save. With a controller reachable, the active step outlines and its name
highlights, read from the retained step slot exposed in `/api/state`.

**nautilus: Diff SFC Diagram (vs git HEAD)** and **(vs Controller)** overlay
added, removed, and changed elements on the chart.

## Tooling

`nautilus new --language sfc` scaffolds a project with a starter chart.
`nautilus check` gates it in CI, and `nautilus test` runs the same
`*_test.yaml` acceptance tests every other language uses, in virtual time.
`nautilus sfc graph <file>` emits the render model as JSON, each transition
carrying its derived kind (`normal`/`alt`/`simDiverge`/`simConverge`), and
`nautilus sfc edit` applies one structural op to source read on stdin.

Step activity, stored flags, pulse edge memories, and step timers are all
retained VAR slots, so an [online edit](/guides/online-edits/) preserves the
live token and every timer, and a running batch does not restart. Renaming a
step is the exception: the renamed step's slot is a new name, so its token
resets, listed in the swap report the way an FB-instance rename is.
**Pull Program from Controller** and `nautilus pull` bring a field edit back
into the `.sfc` file for review in `git diff`.

## Not supported

- The timed qualifiers `L`, `D`, `SD`, `DS`, `SL`. They parse and are
  reported as an error naming the supported set.
- Macro steps and charts nested inside a step. Charts are flat.
- Explicit numeric transition priorities; priority is declaration order.
- Instruction List action bodies. Action bodies are ST.
- Transition indicator variables, and the full standard action-control block.
- Import of vendor SFC (Rockwell `.L5X` routines, Siemens GRAPH, CODESYS).
- Machine-checked token conservation for pathological branch topologies. The
  structural checks cover the detectable cases.

## See also

- [Structured Text](/languages/structured-text/),
  [Ladder](/languages/ladder/),
  [Function Block Diagram](/languages/function-block/)
- [Function blocks, libraries, and tasks](/guides/blocks-and-tasks/)
- [Online edits](/guides/online-edits/)
- [Testing](/reference/testing/)
- [Language reference](/reference/functions/)
