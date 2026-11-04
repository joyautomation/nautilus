PROGRAM Traffic
(* Four-way light on one 50 s cycle timer; each lamp is a window of its ET. *)
LD
  RUNG cycle (* the cycle timer restarts itself one scan after it finishes *)
    Enable /CycleDone cycTmr:TON(PT := T#50S) ( CycleDone )

  RUNG nsgreen (* 0-20 s *)
    Enable LT(cycTmr.ET, T#20S) ( NS_Green )

  RUNG nsyellow (* 20-23 s *)
    Enable GE(cycTmr.ET, T#20S) LT(cycTmr.ET, T#23S) ( NS_Yellow )

  RUNG nsred (* red whenever north-south is not green or yellow *)
    /NS_Green /NS_Yellow ( NS_Red )

  RUNG ewgreen (* 25-45 s *)
    Enable GE(cycTmr.ET, T#25S) LT(cycTmr.ET, T#45S) ( EW_Green )

  RUNG ewyellow (* 45-48 s *)
    Enable GE(cycTmr.ET, T#45S) LT(cycTmr.ET, T#48S) ( EW_Yellow )

  RUNG ewred (* red whenever east-west is not green or yellow *)
    /EW_Green /EW_Yellow ( EW_Red )
END_LD
END_PROGRAM
