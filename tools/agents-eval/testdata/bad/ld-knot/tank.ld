PROGRAM Tank
LD
  RUNG r1
    [ [ StartPB StopPB | Pump StopPB ] /Overload | AutoSel /LSH101 StopPB /Overload ] /LSHH101 ( Pump )

  RUNG r2
    LSHH101 ( S Horn )

  RUNG r3
    LSHH101 hhTmr:TON(PT := T#2S) /HornAck ( Horn )

  RUNG r4
    HornAck ( R Horn )

  RUNG r5
    AutoSel /LSL101 /LSH101 /LSHH101 /Overload StopPB /HHAlarm Ready ( Fill )

  RUNG r6
    Fill { Total := Total + Rate; Avg := Total / 2.0 }
END_LD
END_PROGRAM
