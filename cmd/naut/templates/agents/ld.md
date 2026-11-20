## Ladder (`.ld`)

```
PROGRAM Interlocks
(* Turns the sequence's requests into commands, or refuses them. *)
LD
  RUNG fillcmd (* fill only while asked and the high-high switch is clear *)
    FillReq /LSHH101 ( XV101_Cmd )

  RUNG draincmd (* draining is always allowed *)
    DrainReq ( XV102_Cmd )

  RUNG heatperm (* the heater needs liquid over it and no trip *)
    GE(LT101_Level, HeatMinLevelSP) /HeaterTrip ( HeatPermissive )

  RUNG hhalarm (* LSHH-101 held for 2 s *)
    LSHH101 hhTmr:TON(PT := T#2S) ( HighHighAlm )

  RUNG horn (* a new alarm sounds the horn; it seals in until acknowledged *)
    [ +HighHighAlm | +HighLevelAlm | Horn ] /HornAck ( Horn )
END_LD
END_PROGRAM
```

Elements: `X` NO contact, `/X` NC contact, `+X` / `-X` rising / falling
edge, `[ a b | c ]` parallel branch (legs are series), `GT(a, b)` function
contact (any comparison), `inst:TON(PT := T#5S)` block in the rung,
`{ Dest := Src }` move, `( X )` coil, `( S X )` / `( R X )` set / reset,
`( P X )` / `( N X )` edge coil. `// text` on its own line is a note.

1. **One rung, one idea, one output.** Several coils on one rung only when
   they really share the whole condition.
2. **Read left to right**: request or mode, then permissives (usually NC
   contacts), then the coil at the right. Seal-in: `[ Start | Run ] /Stop
   ( Run )`.
3. **Keep rungs small**: at most 6 elements in series (LD003), and no branch
   inside a branch (`[ a [ b | c ] | d ]`, LD002). Past that, compute a named
   intermediate BOOL on its own rung (`PumpPermissive`, `PumpAutoCall`) and
   use it as one contact.
4. **Factor, don't repeat**: a contact on every leg of a branch belongs once,
   in series, outside the branch (LD004).
5. **Name and comment every rung**: a real name, plus `(* … *)` saying
   *why*, not restating the contacts (LD005).
6. **One coil per BOOL.** Drive a BOOL with `( X )` on exactly one rung. A
   latch is a `( S X )` / `( R X )` pair; never mix those with `( X )` on the
   same tag (LD001).
7. **Power is boolean.** Comparisons are contacts; `ADD(...)` is not. Numeric
   work goes inside a comparison's arguments or a `{ }` move. More than one
   short assignment means the logic belongs in FBD or ST. Compare against a
   setpoint tag or a named constant, not a number: `GE(Level, HighLevelSP)`,
   not `GE(Level, 90.0)` (LD006).
8. **Timers and counters sit in the rung**: power drives `IN` (`CU`, `CD`,
   `CLK`); never pass it yourself. Capture values with `=>`:
   `c1:CTU(PV := 9999, CV => Starts)`.
9. **Use edge contacts** (`+X`) instead of hand-built one-shot latches.
10. **Repeated equipment** (two pumps) is a `FUNCTION_BLOCK` with rungs in
    `lib/*.ld`, instantiated once per unit in a rung.
11. **Not supported**: jumps/labels, MCR zones, negated coils `( /X )`, coils
    inside a branch or before a block, vendor mnemonics (`XIC`, `OTE`,
    `GRT`, `.DN`). ASCII-art ladder is not a file format.

