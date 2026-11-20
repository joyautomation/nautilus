---
title: Four languages, one controller
description: "Write the same logic as Structured Text, a function block diagram and a sequential function chart, see ladder for interlocks, and mix them in one project."
sidebar:
  order: 4
---

IEC 61131-3 has four programming languages, and nautilus runs all of them
in one controller. This lesson shows the same small job in three of them, so
you can see what each is for, and how to mix them. About ten minutes. You need
`naut` ([install](/learn/getting-started/install/)).

## The same job, three ways

`naut new` can start a project in any of the four languages:

```sh
naut new p-st  --template minimal --language st
naut new p-fbd --template minimal --language fbd
naut new p-sfc --template minimal --language sfc
naut new p-ld  --template minimal --language ld
```

Each is a complete project, and each passes `naut check` and `naut test`.
The first three do the same thing: the actuator follows how far the sensor
is below the setpoint, clamped to 0 to 100.

**Structured Text** is text, like a programming language:

```st
PROGRAM PSt
VAR
    err : REAL;
END_VAR

err := Setpoint - Sensor;
Actuator := LIMIT(0.0, err, 100.0);
END_PROGRAM
```

**Function block diagram** is wires and blocks, written as a netlist: each
line is a wire (`name = expression`) or a coil that writes a tag
(`name := expression`):

```fbd
FBD
  w_err = SUB(Setpoint, Sensor)
  Actuator := LIMIT(0.0, w_err, 100.0)
END_FBD
```

**Sequential function chart** is steps with actions. This starter has one
step that is always active:

```sfc
SFC

  INITIAL_STEP Track:
    N  UpdateActuator;
  END_STEP

  ACTION UpdateActuator:
    Actuator := LIMIT(0.0, Setpoint - Sensor, 100.0);
  END_ACTION

END_SFC
```

Steps are where an SFC earns its keep: a sequence (fill, heat, hold,
drain) is a chart of steps, and a **transition** between two steps is the
condition that moves on.

**Ladder** (`--language ld`) is for interlocks. Its starter raises an alarm
when the sensor passes the setpoint:

```text
LD
  RUNG high (* alarm when the sensor exceeds the setpoint *)
    GT(Sensor, Setpoint) ( Alarm )
END_LD
```

Check the four: `naut check` in each folder should say `0 with errors`, and
`naut test` two passing tests.

## Which one to use

| Logic | Language | Why |
| --- | --- | --- |
| Interlocks, permissives, latches, annunciators | [Ladder](/languages/ladder/) | reads as the relay logic it replaces |
| Loops, signal conditioning, continuous control | [Function block diagram](/languages/function-block/) | data flows between blocks |
| Math, arrays, structs, anything algorithmic | [Structured Text](/languages/structured-text/) | the full language; the others lower to it |
| Batch sequences, startup and shutdown | [Sequential function chart](/languages/sfc/) | steps and transitions are the spec |

## Mix them in one project

You choose a language per task, and a controller can have several tasks. The
default project from `naut new` runs three:

```yaml
tasks:
  - program: program.fbd   # the control loop: pump seal-in + heater PI
    scan: 100ms
  - name: sim              # the plant, simulated in IEC
    program: sim.st
    scan: 100ms
  - name: interlocks       # the annunciator, in ladder
    program: interlocks.ld
    scan: 200ms
```

The control loop is a function block diagram, the simulated plant is
structured text and the alarms are ladder, all against one set of tags. Nothing
about a tag, a test or a screen depends on which language wrote the logic.
(Run `naut new plant` to get this project; [two tasks, two scan
rates](/learn/programming/two-tasks-two-scan-rates/) explains the `tasks:`
list.)

## See the diagrams

The graphical languages are text first. With the VS Code extension, right-click
a `.ld`, `.fbd` or `.sfc` file and choose **Open With**, then the ladder, FBD
or SFC diagram: every change you make there is an edit to the text, so it
shows in `git diff` as a few readable lines.

## Next

Each language has its own page: [Structured Text](/languages/structured-text/),
[Ladder](/languages/ladder/), [Function Block
Diagram](/languages/function-block/) and [Sequential Function
Chart](/languages/sfc/). [Four languages, one program model](/languages/) is the
overview, and the [Studio 5000 map](/coming-from/studio-5000/) is the way in
from Logix ladder.
