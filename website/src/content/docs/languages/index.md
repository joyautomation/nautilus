---
title: Four languages, one program model
description: Structured Text, Ladder, Function Block Diagram, and Sequential Function Chart are all plain text in nautilus, and all four compile to the same IR.
sidebar:
  order: 0
  label: Overview
---

A nautilus program file is `.st`, `.ld`, `.fbd`, or `.sfc`. Pick the
language per task and mix them freely in one controller: the demo project
runs its control loop in FBD, its interlocks in ladder, and its plant
simulation in ST, all against one tag store.

## Text first

IEC 61131-3 defines ladder, function block, and sequential function chart
graphically. nautilus gives each one a text form that carries the standard's
semantics with no vendor dialect: rung text for ladder, a netlist for FBD,
and the standard's own textual keywords for SFC. The text file is the
program. The VS Code extension projects it into a full diagram editor where
every gesture is a structural edit to the text underneath, so a wiring
change, a new rung, or a new step shows up in `git diff` as a few readable
lines.

## One compile chain

Each language lowers one hop toward Structured Text, and ST lowers to the
typed IR the runtime executes:

```
 .st  ─────────────────────────►  IR
 .fbd ── transpile ──► ST ──────►  IR
 .ld  ── transpile ──► FBD ──► ST ─►  IR
 .sfc ── transpile ──► ST ──────►  IR
```

Because the hops preserve line maps, a diagnostic anywhere in the chain
lands on your source: an `.ld` error points at the rung, an `.sfc` error at
the step or transition. And because everything ends in one IR, the whole
toolchain is shared. Built-in functions and function blocks, user
`FUNCTION_BLOCK`s from library files, arrays and structs, `nautilus check`,
acceptance tests, live values, online edits, and visual diffs behave the
same in every language.

## Which language for which job

| Logic | Language | Why |
| --- | --- | --- |
| Interlocks, permissives, latches, annunciators | [Ladder](/languages/ladder/) | Reads as the relay logic it replaces; power flow is the review |
| Loops, signal conditioning, continuous control | [Function Block Diagram](/languages/function-block/) | Data flow between blocks is the natural shape |
| Math, arrays, structs, physics, anything algorithmic | [Structured Text](/languages/structured-text/) | The full language; the other three lower to it |
| Batch sequences, state machines, startup and shutdown | [Sequential Function Chart](/languages/sfc/) | Steps and transitions are the spec of the process |

`nautilus new --language ld` (or `fbd`, `sfc`) scaffolds a minimal project
in that language. Tasks are declared in `nautilus.yaml`, one program file
each, at their own scan rate. See [Function blocks, libraries, and
tasks](/guides/blocks-and-tasks/) for structuring logic across files and
tasks, and the [language reference](/reference/functions/) for evaluation
semantics and every built-in.
