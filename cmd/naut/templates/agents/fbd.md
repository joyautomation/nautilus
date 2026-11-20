## Function Block Diagram (`.fbd`)

```
PROGRAM Control
(* TIC-101 heater loop and the high level alarm. *)
FBD
  NETWORK 'TIC-101: heater temperature loop'
  // reverse acting (DIRECT := FALSE); off auto, CV follows CV_MAN (0)
  heatOn = AND(HeatReq, HeatPermissive)
  tic : PID(AUTO := heatOn, PV := TT101_Temp, SP := TempSP,
            KP := TicKp, KI := TicKi, CV_MIN := 0.0, CV_MAX := 100.0,
            DIRECT := FALSE)
  E101_Out := tic.CV

  NETWORK 'LAH-101: high level, 10 s on-delay'
  hiLevel = GE(LT101_Level, HighLevelSP)
  hlTmr : TON(IN := hiLevel, PT := T#10S)
  HighLevelAlm := hlTmr.Q
END_FBD
END_PROGRAM
```

Forms: `wire = BLOCK(...)` names a block output; `Tag := expr` is a coil
(writes a variable); `inst : TYPE(Pin := ...)` declares and calls a block;
`inst.Pin` reads an output; `NOT x` negates a pin; `NETWORK 'title'` starts
a network.

1. **One `NETWORK 'title'` per function**: one loop, one alarm, one scaling.
   Each draws as its own titled band (FBD002).
2. **One block per line.** Give each intermediate result a wire named for
   what it carries (`hiLevel`, `err`, `heatOn`), not `w1`. Nest calls at
   most two deep (FBD001); a long nested expression draws as a wide tree of
   crossing wires.
3. **Write in signal order**: wires from inputs, then block calls, then the
   coils that write tags.
4. **Compare once, reuse the wire.** One variable feeding many blocks in a
   network draws long crossing wires; one named comparison does not. Compare
   against a setpoint tag or a named constant, not a number (FBD003).
5. **Use the library block** (`PID`, `LIMIT`, `SEL`, `TON`, `R_TRIG`, `CTU`,
   `AND`/`OR` with several inputs) instead of rebuilding it from math, and
   do not add blocks for what the block already does.
6. **No operators and no control flow.** Write `ADD(a, b)`, `GT(a, b)`,
   `AND(a, b, c)`, never `a + b` or `a > b` (the parser rejects them: "FBD
   has no operators"). No `IF`, `CASE` or loops: that is ST.
7. **Feedback goes through a variable**: a seal-in is
   `Alm := AND(OR(set, Alm), NOT clear)`. A wire that reads itself is an
   error.
8. **Never write a `(* @layout … *)` block.** It holds positions a person
   dragged in the editor. Do not invent or edit coordinates; leave an
   existing block alone.

