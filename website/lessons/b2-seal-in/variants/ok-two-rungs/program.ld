PROGRAM Motor
VAR Want : BOOL; END_VAR
LD
  RUNG want
    [ StartPB | Want ] /StopPB ( Want )
  RUNG run
    Want ( MotorRun )
END_LD
END_PROGRAM
