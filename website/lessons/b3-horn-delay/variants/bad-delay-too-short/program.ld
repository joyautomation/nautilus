PROGRAM Motor
VAR
    Starting : BOOL;   (* TRUE from pressing Start until the motor runs *)
    t1       : TON;    (* the 5 second warning *)
END_VAR
LD
  RUNG starting (* Start begins the warning; Stop or a running motor ends it *)
    [ StartPB | Starting ] /StopPB /MotorRun ( Starting )
  RUNG warning (* five seconds of Starting *)
    Starting t1:TON(PT := T#2S)
  RUNG horn
    Starting ( Horn )
  RUNG run (* the motor starts when the warning ends, and seals itself in *)
    [ t1.Q | MotorRun ] /StopPB ( MotorRun )
END_LD
END_PROGRAM
