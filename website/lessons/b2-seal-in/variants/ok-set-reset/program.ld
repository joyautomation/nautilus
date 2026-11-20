PROGRAM Motor
LD
  RUNG start_it
    StartPB /StopPB ( S MotorRun )
  RUNG stop_it
    StopPB ( R MotorRun )
END_LD
END_PROGRAM
