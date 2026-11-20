PROGRAM Motor
VAR
    Starting : BOOL;   (* TRUE from pressing Start until the motor runs *)
    t1       : TON;    (* the 5 second warning *)
END_VAR
LD
  RUNG seal_in (* TODO: now Start should only begin a 5 second warning (Starting) *)
    [ StartPB | MotorRun ] /StopPB ( MotorRun )
END_LD
END_PROGRAM
