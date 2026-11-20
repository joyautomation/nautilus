---
title: "Count things: edges and counters"
track: programming
next: none
open: [program.st]
---

A photo-eye over a conveyor goes TRUE while a parcel blocks its beam. This
lesson counts parcels: `Count` is how many have passed, `Reset` sets it back
to zero, and `Full` comes on at five.

## The scan trap

You could write `Count := Count + 1` whenever `Eye` is TRUE. It would be
wrong: the program runs every 100 ms, and a parcel blocks the beam for many
scans, so one parcel would count again on every scan it is in the beam. What
you want to count is the **moment** the beam goes from clear to blocked: a
**rising edge**, TRUE for exactly one scan.

## The counter block

`CTU` (count up) is the standard block for this. Call it once per scan:

```st
parcels(CU := Eye, R := Reset, PV := 5);
```

- `CU` is the count input. `CTU` counts each rising edge of `CU` by itself, so
  you do not detect the edge yourself.
- `R` resets the count to 0 while it is TRUE.
- `PV` is the preset.
- `parcels.CV` is the count so far, and `parcels.Q` turns TRUE once
  `CV >= PV`.

## Your turn

Complete the two TODOs in `program.st`:

1. Call `parcels(CU := Eye, R := Reset, PV := 5);`.
2. Copy the count to the output tag, `Count := parcels.CV;`, and set
   `Full := parcels.Q;`.

Click `Eye` in the **Tags** panel to TRUE and back to FALSE to send a parcel;
hold it TRUE for a few seconds and see that it counts once.

Press **Check**. It sends parcels in virtual time and tells you which of
these failed: a parcel blocking the beam for several scans counts once, three
parcels count three, Reset sets 0, Full is off at four and on at five, and the
count keeps going past five.

Other correct programs pass too: a `R_TRIG` in front of the counter, or an
`INT` you add to yourself with a remembered "was the eye blocked last scan"
flag.

## Stuck?

**Solve** loads the three-line answer.
