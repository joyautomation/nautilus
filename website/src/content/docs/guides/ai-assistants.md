---
title: AI assistants and AGENTS.md
description: Every new manifest project ships an AGENTS.md that tells Codex, Claude Code and other coding agents how to write ladder, FBD and SFC that draw as clean diagrams, and how to check their work with naut check and naut test.
---

nautilus programs are text, so a coding agent can write all four IEC
languages. Whether the ladder, FBD or SFC it writes reads as a diagram
someone would recognize is another matter. Every manifest project
`naut new` makes (the `minimal` and `demo` templates, the same two that
**nautilus: Create Project…** in VS Code offers as **Minimal** and
**Demo**) carries an `AGENTS.md` that tells the agent how. Codex, Claude
Code and the other agents that read `AGENTS.md` pick it up on their own;
the project's `CLAUDE.md` only imports it. The Go SDK templates (`sdk`,
`sdk-demo`) do not get one: they have no `nautilus.yaml`, so the file's
check loop and tag rules would not apply. Its ladder, FBD and SFC rules
still do, and [`naut agents`](#an-existing-project) adds it.

## Why a diagram can compile and still look wrong

Nobody places anything in a nautilus diagram. The ladder editor lays out
rungs canonically, and the FBD and SFC editors lay out from the program's
topology. So the agent never has to invent coordinates, and it cannot get
them wrong. What it does choose is the *structure*, and the drawing shows
every choice. Left to itself, a model writes ladder and FBD the way it
writes Structured Text, which is valid and hard to read:

- **Everything in one rung.** Three levels of nested branches, the same
  contact repeated on several legs, and two coils on the end. It compiles,
  and it draws as a tall knot. The fix is one output per rung, no branch
  inside a branch, and the shared conditions computed once on a rung of
  their own:

  ```iecld
  RUNG pumpperm (* the pump may run: stop not pressed, no overload *)
    /StopPB /Overload ( PumpPermissive )

  RUNG pumphand (* hand: the start button, sealed in by the running pump *)
    HandMode [ StartPB | Pump ] ( PumpHandCall )

  RUNG pump (* run on the hand or the auto call, while permitted *)
    [ PumpHandCall | PumpAutoCall ] PumpPermissive ( Pump )
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
  bugs, and `naut test` for any change in behavior. Most of the diagram
  rules below are also [structure warnings](/reference/structure-warnings/)
  that `naut check` reports by ID (`LD001`, `FBD002`, `SFC001`), and the
  file tells the agent to fix the diagram rather than silence one with an
  `@allow` pragma.
- **Project layout** and the **tag model**: roles, an `init:` and a `desc:`
  on every tag, one writer per tag, and no `VAR` declarations that shadow
  a manifest tag.
- **Which language for which logic**, and the asks/decides split between
  SFC and ladder.
- **Ladder:** one idea per rung, inputs left and coil right, at most about
  six elements in series and no branch inside a branch, a comment on
  every rung, one coil per BOOL, the IEC element set and what is not
  supported.
- **FBD:** a titled network per function, one block per line, named wires,
  signal order, library blocks over hand-built math, and never an invented
  `(* @layout *)` block.
- **SFC:** process-named steps, conditions in transitions, outputs as
  associations (an `ACTION` body only for what an association cannot do,
  such as a counter), abort paths declared first, `Step.T` and `MAXTIME`
  for time.
- **Acceptance tests** in virtual time.

Its examples (a tank's sequence, interlocks and heater loop) are checked in
CI: `naut new` ships the file, and a test compiles and runs the examples it
shows, so the guidance cannot drift from what works.

## An existing project

`naut agents` adds the file to a project that predates it, or to one
imported from elsewhere:

```sh
naut agents          # writes AGENTS.md, and a CLAUDE.md that imports it
naut agents -check   # says whether either is missing or stale; writes nothing
```

The text comes from your `naut`, so it always matches the checks that
`naut` runs. In VS Code, opening a project that has no `AGENTS.md`, or an
older naut's copy, offers to add or update it (turn that off with the
`nautilus.agentsMd.offer` setting); **nautilus: Add or Update AGENTS.md**
in the Command Palette does the same at any time.

## Keeping it current

The nautilus text sits between two markers:

```markdown
<!-- nautilus:begin (naut 0.21.0): … -->
# AGENTS.md
…
<!-- nautilus:end -->
```

When a newer `naut` has newer guidance, `naut check` warns that the
project's copy is stale, and `naut agents` replaces what is between the
markers. Anything above or below them is yours and is kept, so that is
where your site's own conventions go: tag naming, which programs own which
equipment, what a rung comment must say.

A copy made by an older `naut new`, which had no markers, is recognised and
updated the same way, and notes you added below it are kept below the new
text. If you edited *inside* an older copy, `naut` cannot tell its text
from yours: `naut check` says so, and you move your notes above or below
the nautilus text before `naut agents -force` replaces it. An `AGENTS.md`
that `naut` did not write at all is left alone: `naut agents -append` adds
the nautilus guidance below its text, and `-force` replaces the file. A `CLAUDE.md` that exists without importing
`AGENTS.md` gets the `@AGENTS.md` line added; nothing else in it changes.

A project with no `AGENTS.md` gets no warning from `naut check`. Plenty of
projects have no use for one, and a warning that never goes away teaches
people to ignore warnings.

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
  logic. It does catch an output with no `init:` that a program reads
  before writing it (a seal-in), which would fault the scan. It does not
  catch logic that is legal and wrong, such as a BOOL driven by coils on
  two rungs, where the later rung wins. `naut test` and review catch that.

## See also

- [Ladder Diagram](/languages/ladder/) ·
  [Function Block Diagram](/languages/function-block/) ·
  [Sequential Function Chart](/languages/sfc/)
- [The tag model](/guides/tag-model/)
- [Testing](/reference/testing/)
- [Your first program](/guides/first-program/)
