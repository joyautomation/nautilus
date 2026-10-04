PROGRAM MainProgram
(* DemoLine: the two rungs on the Echo controller, written as nautilus
   ladder. The tags are the project's (nautilus.yaml); on the controller
   they are controller-scope tags with the same names. *)
VAR_EXTERNAL
    StartPB    : BOOL;
    StopPB     : BOOL;
    RunCmd     : BOOL;
    HiLevelAlm : BOOL;
    LevelPct   : REAL;
    HiLevelSP  : REAL;
END_VAR
LD
  RUNG seal (* P-101 pump seal-in: Start latches the run command, Stop drops it out *)
    [ StartPB | RunCmd ] /StopPB ( RunCmd )

  RUNG alarm (* LAH-101 high-level alarm when tank level is at or above the setpoint *)
    GE(LevelPct, HiLevelSP) ( HiLevelAlm )
END_LD
END_PROGRAM
