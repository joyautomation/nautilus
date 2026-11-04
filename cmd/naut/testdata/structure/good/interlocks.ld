PROGRAM Interlocks
(* Turns the sequence's requests into commands, or refuses them. *)
LD
  RUNG fillcmd (* fill only while asked and the high-high switch is clear *)
    FillReq /LSHH101 ( XV101_Cmd )

  RUNG draincmd (* draining is always allowed *)
    DrainReq ( XV102_Cmd )

  RUNG heatperm (* the heater needs liquid over it and no trip *)
    GE(LT101_Level, HeatMinLevelSP) /HeaterTrip ( HeatPermissive )

  RUNG hhalarm (* LSHH-101 held for 2 s *)
    LSHH101 hhTmr:TON(PT := T#2S) ( HighHighAlm )

  RUNG horn (* a new alarm sounds the horn; it seals in until acknowledged *)
    [ +HighHighAlm | +HighLevelAlm | Horn ] /HornAck ( Horn )
END_LD
END_PROGRAM
