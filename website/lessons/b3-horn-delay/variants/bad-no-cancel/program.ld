PROGRAM Motor
VAR Starting : BOOL; t1 : TON; END_VAR
LD
  RUNG a
    [ StartPB | Starting ] /MotorRun ( Starting )
  RUNG b
    Starting t1:TON(PT := T#5S)
  RUNG h
    Starting ( Horn )
  RUNG c
    [ t1.Q | MotorRun ] /StopPB ( MotorRun )
END_LD
END_PROGRAM
