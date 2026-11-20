---
title: "A timer: warn before the motor starts"
track: programming
next: none
carry: [program.ld]
open: [program.ld]
---

Real motors often sound a horn for a few seconds before they start, so
nobody is standing at the machine. In this lesson Start no longer starts the
motor at once: it sounds a horn, and the motor starts five seconds later.

You build on the seal-in from the last lesson (your `program.ld` carries
over). The new tag is `Horn`.

## The behaviour

- Pressing Start sounds the horn (`Horn` is TRUE) at once.
- Five seconds after Start, the motor starts (and stays on, as before) and
  the horn stops.
- Stop during the warning cancels it: the horn stops and the motor never
  starts. Stop also stops a running motor.

## The pieces

**A variable that remembers.** The `VAR` section declares two locals that
keep their value between scans: `Starting`, TRUE from pressing Start until
the motor runs, and `t1`, a `TON` timer.

**A timer in a rung.** `t1:TON(PT := T#5S)` drops a timer into a rung. Power
reaching it runs the timer; its output, `t1.Q`, turns TRUE after five
seconds of power. Use that output as a contact (`t1.Q`) elsewhere.

## Your turn

Replace the seal-in rung with four rungs:

1. `Starting` seals itself in from Start, and ends on Stop or when the motor
   runs.
2. A rung that runs the timer while `Starting` is TRUE.
3. `Starting` drives `Horn`.
4. The motor seals in from `t1.Q`, instead of from Start, and Stop still
   breaks it.

Press **Check**. It starts the motor in virtual time, so five seconds take no
real time, and tells you which part is wrong: the horn at once, the motor
not started at 4.5 s, running by 5.5 s with the horn off, still running at
17 s, Stop cancelling a warning, and Stop stopping a running motor.

## Stuck?

**Solve** loads the four-rung answer. Try changing `T#5S` to `T#2S` and press
Check: it tells you the motor started too soon.
