PROGRAM MainProgram
(* DemoLine: the two rungs on the Echo controller, written as nautilus
   ladder. The L5X the writer emits for this is compared rung for rung
   with the export Studio 5000 made of the same logic. *)
VAR
    StartPB    : BOOL;
    StopPB     : BOOL;
    RunCmd     : BOOL;
    HiLevelAlm : BOOL;
    LevelPct   : REAL;
    HiLevelSP  : REAL := 85.0;
END_VAR
LD
  RUNG seal (* P-101 pump seal-in: Start latches the run command, Stop drops it out *)
    [ StartPB | RunCmd ] /StopPB ( RunCmd )

  RUNG alarm (* LAH-101 high-level alarm when tank level is at or above the setpoint *)
    GE(LevelPct, HiLevelSP) ( HiLevelAlm )
END_LD
END_PROGRAM
