---
title: "First program: blink an output"
track: programming
next: none
open: [program.st]
---

Your first program makes one output, a lamp, blink: about half a second on,
half a second off. It is the smallest program that needs the two ideas
every controller program is built from.

## What is in the project

- `nautilus.yaml` says what the controller is: one **task** that runs
  `program.st` every 100 ms, and one **tag**, `Lamp`. A tag is a named
  value. `Lamp` is an `output`: the program writes it.
- `program.st` is the program, in Structured Text, with two TODOs.

## Idea 1: the scan

A controller does not wait for things to happen. It runs the program from
top to bottom, over and over, once every 100 ms. One run is a **scan**.
Nothing in the program loops by itself: to do something every half second
it counts scans, and that is what a timer is for.

Variables declared in `VAR` keep their value from one scan to the next. That
is how a program remembers anything.

## Idea 2: the on-delay timer

`TON` is a timer with a `Q` output. Call it once per scan:

```st
tick(IN := something, PT := T#500ms);
```

`tick.Q` turns TRUE once `IN` has been TRUE for `PT` (here 500 ms). When `IN`
goes FALSE, the timer starts over.

## Your turn

1. **TODO 1.** Run the stopwatch, and make it restart when it finishes. A
   timer restarts when `IN` goes FALSE, and `tick.Q` is TRUE at the moment
   it finishes, so feed it the opposite of its own output:
   `tick(IN := NOT tick.Q, PT := T#500ms);`
2. **TODO 2.** When the stopwatch finishes (`tick.Q`), flip the lamp:

   ```st
   IF tick.Q THEN
       Lamp := NOT Lamp;
   END_IF;
   ```

Watch it live: the value of `Lamp` flips in the **Tags** panel while the
controller runs.

Press **Check**. It runs the program in virtual time, so a 3-second test
takes no real time, and tells you which of these failed: the lamp must stay
off for the first half second, then be on, then off, then on again.

The blink is a little slower than 500 ms each way, about 600 to 700 ms. The
timer needs a scan to finish and a scan to start over. That is normal, and
the check allows for it.

## Stuck?

Press **Solve** to load the solution, then read it. Try changing `T#500ms`
to `T#250ms` and watch the lamp blink faster.
