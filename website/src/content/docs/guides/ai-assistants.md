---
title: AI assistants and AGENTS.md
description: Every new project ships an AGENTS.md that tells Codex, Claude Code and other coding agents how to write ladder, FBD and SFC that draw as clean diagrams, and how to check their work with naut check and naut test.
---

nautilus programs are text, so a coding agent can write all four IEC
languages. Whether the ladder, FBD or SFC it writes reads as a diagram
someone would recognize is another matter. Every project `naut new` makes
(and so every project from **nautilus: Create Project…** in VS Code)
carries an `AGENTS.md` that tells the agent how. Codex, Claude Code and
the other agents that read `AGENTS.md` pick it up on their own; the
project's `CLAUDE.md` only imports it.

## Why a diagram can compile and still look wrong

Nobody places anything in a nautilus diagram. The ladder editor lays out
rungs canonically, and the FBD and SFC editors lay out from the program's
topology. So the agent never has to invent coordinates, and it cannot get
them wrong. What it does choose is the *structure*, and the drawing shows
every choice. Left to itself, a model writes ladder and FBD the way it
writes Structured Text, which is valid and hard to read:

- **Everything in one rung.** Three levels of nested branches, the same
  contact repeated on several legs, and two coils on the end. It compiles,
  and it draws as a tall knot. The fix is one output per rung, with the
  shared conditions computed once on a rung of their own:

  ```iecld
  RUNG pumpperm (* the pump may run: stop not pressed, no overload *)
    /StopPB /Overload ( PumpPermissive )

  RUNG pump (* hand: start/stop seal-in; auto: the fill call *)
    [ HandMode [ StartPB | Pump ] | PumpAutoCall ] PumpPermissive ( Pump )
  ```

- **ST expressions dressed as FBD.** A single statement such as
  `Pump := AND(AND(OR(StartPB, Pump), NOT StopPB), AND(NOT Overload, ...))`
  draws as a wide tree, and every variable it reads in several places pulls
  long wires across the network. One block per line, each intermediate
  wire named for what it carries, and a titled `NETWORK` per function make
  the same logic draw as a left-to-right chain.
- **A state machine dressed as SFC.** Steps `S0` to `S3`, transitions on a
  `State` integer, and the real conditions in `IF` statements inside the
  action bodies. The chart then shows `State = 2` and nothing else. In
  SFC the condition belongs in the transition and the outputs in `N`
  associations.
- **The wrong language.** PID math in a ladder `{ … }` block, interlocks as
  nested `AND`/`OR` in FBD. The examples split a machine by language:
  sequence in SFC, interlocks in ladder, loops in FBD, math in ST.
- **Magic numbers and missing comments.** `GT(Level, 90.0)` instead of a
  setpoint tag, rungs named `r1`, no rung comment. Tag descriptions print
  under every contact and coil, so tags with a `desc:` make the diagram
  self-documenting.

## What AGENTS.md covers

The file is short, because an agent reads it on every turn:

- **Checking work:** `naut check` after every edit, warnings treated as
  bugs, and `naut test` for any change in behavior.
- **Project layout** and the **tag model**: roles, an `init:` and a `desc:`
  on every tag, one writer per tag, and no `VAR` declarations that shadow
  a manifest tag.
- **Which language for which logic**, and the asks/decides split between
  SFC and ladder.
- **Ladder:** one idea per rung, inputs left and coil right, at most about
  six elements in series and one level of branch nesting, a comment on
  every rung, one coil per BOOL, the IEC element set and what is not
  supported.
- **FBD:** a titled network per function, one block per line, named wires,
  signal order, library blocks over hand-built math, and never an invented
  `(* @layout *)` block.
- **SFC:** process-named steps, conditions in transitions, outputs as
  associations, abort paths declared first, `Step.T` and `MAXTIME` for time.
- **Acceptance tests** in virtual time.

Its examples (a tank's sequence, interlocks and heater loop) are checked in
CI: `naut new` ships the file, and a test compiles and runs the examples it
shows, so the guidance cannot drift from what works.

## An existing project

Projects made before the file shipped can take it as is:

```sh
curl -fsSL -o AGENTS.md \
  https://raw.githubusercontent.com/joyautomation/nautilus/main/cmd/naut/templates/AGENTS.md.tmpl
printf 'Read AGENTS.md before changing any program:\n\n@AGENTS.md\n' > CLAUDE.md
```

It is a starting point. Add your site's own conventions (tag naming,
which programs own which equipment, what a rung comment must say) to the
same file.

## Working with an agent

- **Name the language per program** in the request ("the interlocks in
  ladder, the loop in FBD"). The agent follows `AGENTS.md`'s table
  otherwise.
- **Ask for the test first.** A `*_test.yaml` that states the behavior in
  virtual time ("the alarm waits its full 10 s") gives the agent something
  to run, and gives you something to read that is shorter than the logic.
- **Review in the diagram.** Open the result with **Open With → Ladder
  Diagram** (or FBD/SFC). A rung that is hard to follow there is the thing
  to send back, whatever the text looks like.
- **Watch what `naut check` cannot see.** It compiles; it does not run the
  logic. A tag read before anything writes it faults at run time, and a
  BOOL driven by coils on two rungs is legal. `naut test` catches the
  first; review catches the second.

## See also

- [Ladder Diagram](/languages/ladder/) ·
  [Function Block Diagram](/languages/function-block/) ·
  [Sequential Function Chart](/languages/sfc/)
- [The tag model](/guides/tag-model/)
- [Testing](/reference/testing/)
- [Your first program](/guides/first-program/)
