PROGRAM Motor
VAR t1 : TON; END_VAR
LD
  RUNG warn
    [ StartPB | Horn ] /StopPB /MotorRun ( Horn )
  RUNG timer
    Horn t1:TON(PT := T#5S)
  RUNG run
    [ t1.Q | MotorRun ] /StopPB ( MotorRun )
END_LD
END_PROGRAM
