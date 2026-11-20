## Sequential Function Chart (`.sfc`)

```
PROGRAM Sequence
(* Batch: fill, heat, drain. The chart only asks (FillReq, HeatReq,
   DrainReq); interlocks.ld decides whether the equipment runs. *)
SFC
  INITIAL_STEP Idle:
  END_STEP

  STEP Fill (MAXTIME := T#10M, ERROR := FillOverrun):
    N FillReq;
  END_STEP

  STEP Heat:
    N HeatReq;
  END_STEP

  STEP Drain:
    N DrainReq;
    P0 CountBatch;  (* once, as Drain ends *)
  END_STEP

  TRANSITION t_start FROM Idle TO Fill := Start;
  END_TRANSITION

  (* abort first: on a shared source step, declaration order is priority *)
  TRANSITION t_fill_abort FROM Fill TO Drain := Abort OR Fill.ERR;
  END_TRANSITION
  TRANSITION t_full FROM Fill TO Heat := LT101_Level >= FullSP;
  END_TRANSITION

  TRANSITION t_heat_abort FROM Heat TO Drain := Abort;
  END_TRANSITION
  TRANSITION t_hot FROM Heat TO Drain := TT101_Temp >= TempSP AND Heat.T >= T#30S;
  END_TRANSITION

  TRANSITION t_empty FROM Drain TO Idle := LT101_Level <= EmptySP;
  END_TRANSITION

  (* by convention, ACTION bodies go last, before END_SFC *)
  ACTION CountBatch:
    BatchCount := BatchCount + 1;
  END_ACTION
END_SFC
END_PROGRAM
```

1. **Steps are process states** (`Fill`, `Heat`), not `S1`, `S2` (SFC002).
   Transitions get names too (`t_full`).
2. **The condition lives in the transition**, written in process terms. Never
   keep a `State` integer that actions set and transitions compare: the
   chart then shows `State = 2` and hides the logic (SFC001).
3. **Drive outputs with associations** (`N FillReq;`). Use an `ACTION`
   body (ST) only for what an association cannot do, such as a counter
   (`P0 CountBatch;` above); keep it short, with no `IF` deciding where the
   chart goes next. `ACTION name: … END_ACTION` blocks go inside the chart;
   put them after the transitions, so the chart reads steps first.
4. **One `INITIAL_STEP`**, a safe idle with no outputs. Every step reachable.
5. **Alternatives**: transitions sharing a source fire by declaration order,
   so declare abort and fault paths first.
6. **Time**: `Step.T >= T#30S` in a condition; supervise a step with
   `(MAXTIME := T#10M, ERROR := Tag)` and route `Step.ERR` to an abort.
7. **Parallel branches**: `TO (A, B)` splits, `FROM (A, B)` joins.
8. Qualifiers: `N`, `S`, `R`, `P1`, `P0`, `P`, and timed `L`, `D`, `SD`,
   `DS`, `SL` (`D Vent(T#3S);`). Several steps may `N` the same BOOL; they
   OR together.
