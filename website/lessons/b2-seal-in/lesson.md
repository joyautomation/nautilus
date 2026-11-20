---
title: "A motor start/stop in ladder (the seal-in)"
track: programming
next: b3-horn-delay
open: [program.ld]
---

The first circuit every controls engineer learns: a Start button that
starts a motor, a Stop button that stops it, and a motor that **stays on
when you let go of Start**. It is ladder logic, written as text, and you
can also open it as a diagram.

## What is in the project

- `nautilus.yaml`: three tags. `StartPB` and `StopPB` are the buttons
  (`setpoint` tags, so you can press them from the **Tags** panel by
  clicking a value; they are TRUE while pressed). `MotorRun` is the motor
  contactor, an `output`: the program writes it.
- `program.ld`: the program, one rung.

## Reading a rung

A rung runs left to right. Power flows through each **contact** that is
TRUE and ends at a **coil**, which takes the value of the power:

| You write | Means | In Studio 5000 |
| --- | --- | --- |
| `StartPB` | contact: passes power while `StartPB` is TRUE | `XIC` |
| `/StopPB` | contact, normally closed: passes while `StopPB` is FALSE | `XIO` |
| `( MotorRun )` | coil: `MotorRun` is TRUE while power reaches it | `OTE` |
| `[ a \| b ]` | two paths side by side: power passes if either does | a branch |

The starting rung, `StartPB ( MotorRun )`, makes the motor follow the
button: it runs while you hold Start and stops when you let go.

## Your turn

Make the motor keep itself on. Put a second path in parallel with `StartPB`
that is the motor's own contact (`MotorRun`): once the motor runs, it holds
its own rung closed. That is the **seal-in**. Then add the Stop button in
series, as a normally closed contact, so it breaks the rung:

```
[ StartPB | MotorRun ] /StopPB ( MotorRun )
```

If both buttons are pressed together, Stop wins.

Press the buttons in the **Tags** panel while it runs, then press **Check**:
it presses them for you, in virtual time, and tells you which of these
failed: Start turns the motor on; it stays on after Start is released; Stop
turns it off; it stays off until Start is pressed again; Stop wins when both
are pressed.

## Stuck?

**Solve** loads the one-rung answer. Open the file with *Open With →
Ladder Diagram* to see the circuit drawn.
