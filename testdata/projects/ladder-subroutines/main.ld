PROGRAM Main
(* Two pumps, one block. Each `inst:PumpSeq(...)` is an INSTANCE: its own
   seal-in, its own retained TON. The rung's power drives the block's
   first free BOOL input (Start) and continues from its first BOOL output
   (Run); every other pin is bound by name, and `=>` captures an output
   into a tag — the same call shape ST and FBD use. *)
VAR_EXTERNAL
    P101Start : BOOL; P101Stop : BOOL; P101Run : BOOL; P101Warm : BOOL;
    P101Flow  : BOOL; P101FailAlm : BOOL;
    P102Start : BOOL; P102Stop : BOOL; P102Run : BOOL; P102Warm : BOOL;
    P102Flow  : BOOL; P102FailAlm : BOOL;
    LevelPct  : REAL;
    StopLevel : REAL;
    AnyWarm   : BOOL;
END_VAR
VAR
    p101 : PumpSeq;  f101 : PumpFail;
    p102 : PumpSeq;  f102 : PumpFail;
END_VAR
LD
  RUNG lead (* duty pump *)
    P101Start p101:PumpSeq(Stop := P101Stop, Level := LevelPct,
                           StopLevel := StopLevel,
                           Run => P101Run, Warm => P101Warm)

  RUNG leadfail
    P101Run f101:PumpFail(Flowing := P101Flow, Alarm => P101FailAlm)

  RUNG lag (* standby pump — same block, its own state *)
    P102Start p102:PumpSeq(Stop := P102Stop, Level := LevelPct,
                           StopLevel := StopLevel,
                           Run => P102Run, Warm => P102Warm)

  RUNG lagfail
    P102Run f102:PumpFail(Flowing := P102Flow, Alarm => P102FailAlm)

  RUNG anywarm  [ P101Warm | P102Warm ] ( AnyWarm )
END_LD
END_PROGRAM
