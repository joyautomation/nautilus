PROGRAM MainProgram
(* Start latches the run command; the normally closed stop drops it out. *)
VAR_EXTERNAL
    StartPB : BOOL;
    StopPB  : BOOL;
    RunCmd  : BOOL;
END_VAR
LD
  RUNG seal (* pump seal-in *)
    [ StartPB | RunCmd ] StopPB ( RunCmd )
END_LD
END_PROGRAM
