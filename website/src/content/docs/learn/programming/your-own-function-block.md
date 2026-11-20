---
title: Your own function block
description: "Write a reusable block with its own memory, use it twice, and test that the two copies do not share state."
sidebar:
  order: 2
---

A program that controls two pumps should not contain two copies of the same
logic. In IEC 61131-3 the unit of reuse is the **function block**: logic with
its own memory, which you use as many times as you need. You have used
built-in ones already (`TON`, `CTU`); this lesson writes your own. About ten
minutes.

## A block is a type; an instance is a copy

A function block is declared once, and each use is an **instance** with its
own variables. Two `TON`s do not share an elapsed time; two of your blocks
will not share their state either. Put the block in a **library file**, a
file that holds only declarations, in the project's folder (or in a `lib/`
folder). `naut` compiles library files ahead of the programs, so the block is
in scope everywhere without an import.

Make a project (`naut new pumps --template minimal`) and replace its tags in
`nautilus.yaml` with two start buttons, two stop buttons and two run outputs:

```yaml
tags:
  - { name: Start1, role: setpoint, type: BOOL, init: false, desc: Pump 1 start }
  - { name: Stop1,  role: setpoint, type: BOOL, init: false, desc: Pump 1 stop }
  - { name: Run1,   role: output,   type: BOOL, init: false, desc: Pump 1 run }
  - { name: Start2, role: setpoint, type: BOOL, init: false, desc: Pump 2 start }
  - { name: Stop2,  role: setpoint, type: BOOL, init: false, desc: Pump 2 stop }
  - { name: Run2,   role: output,   type: BOOL, init: false, desc: Pump 2 run }
```

## Write the block

Create `blocks.st` beside `nautilus.yaml`:

```st
FUNCTION_BLOCK MotorStarter
VAR_INPUT  Start : BOOL; Stop : BOOL; END_VAR
VAR_OUTPUT Run : BOOL; END_VAR

(* seal-in: Start or already running, and Stop not pressed *)
Run := (Start OR Run) AND NOT Stop;
END_FUNCTION_BLOCK
```

- `VAR_INPUT` are the values the caller hands in; `VAR_OUTPUT` are what it
  gets back.
- `Run` is read on the right of its own assignment: the block *remembers* it
  from one scan to the next, which is what makes it a seal-in. Variables in a
  block keep their value between calls, per instance.

## Use it twice

Replace `program.st`:

```st
PROGRAM Pumps
VAR
    pump1 : MotorStarter;
    pump2 : MotorStarter;
END_VAR

pump1(Start := Start1, Stop := Stop1);
Run1 := pump1.Run;

pump2(Start := Start2, Stop := Stop2);
Run2 := pump2.Run;
END_PROGRAM
```

`pump1 : MotorStarter;` makes an instance. `pump1(Start := …, Stop := …)`
calls it once per scan, passing its inputs, and `pump1.Run` reads its output.

```sh
naut check
```

```text
naut check: 2 file(s), 0 with errors
```

## Test that they are separate

The test that matters for a block is that two instances do not interfere.
In `pumps_test.yaml`:

```yaml
tests:
  - name: each pump seals in on its own
    steps:
      - given: { Start1: true }
        scans: 2
      - given: { Start1: false }
        scans: 2
        expect: { Run1: true, Run2: false }

  - name: stopping one pump leaves the other running
    steps:
      - given: { Start1: true, Start2: true }
        scans: 2
      - given: { Start1: false, Start2: false }
        scans: 2
      - given: { Stop1: true }
        scans: 2
        expect: { Run1: false, Run2: true }
```

```sh
naut test
```

```text
pumps_test.yaml
  ok    each pump seals in on its own
  ok    stopping one pump leaves the other running

2 tests, 2 passed, 0 failed
```

A third pump is one more `VAR` line and one more call, not another copy of
the logic. Fix the logic once in `blocks.st` and every pump has the fix.

## Functions, and other languages

A **function** is a block without memory: it computes a result from its
inputs and keeps nothing (`LIMIT`, `MAX` and your own). Use a function for a
calculation and a block for anything that must remember. A block can also be
written in ladder or function block diagram, and used from any language. The
[function blocks and tasks guide](/guides/blocks-and-tasks/) covers both,
and where library files live.

## Next

[Two tasks, two scan rates](/learn/programming/two-tasks-two-scan-rates/).
