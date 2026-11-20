PROGRAM Motor
LD
  RUNG seal_in (* Start or already running, and Stop not pressed *)
    [ StartPB | MotorRun ] /StopPB ( MotorRun )
END_LD
END_PROGRAM
