PROGRAM Interlocks
LD
  RUNG r1
    [ FillReq /StopPB /Overload | [ Start | Pump ] /StopPB /Overload ] ( Pump )

  RUNG r2
    LSHH101 hhTmr:TON(PT := T#2S) ( S Horn )

  RUNG r3
    GT(LT101_Level, 90.0) ( Horn )

  RUNG r4
    FillReq /LSHH101 /StopPB /Overload /HeaterTrip Start /DrainReq ( XV101_Cmd )

  RUNG r6
    HornAck ( R Horn )
END_LD
END_PROGRAM
