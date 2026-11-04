PROGRAM Tank
(* Tank fill: low/high switch hysteresis, a high-high interlock and alarm. *)
LD
  RUNG fillcall (* below the low switch asks for a fill until the high switch *)
    [ /LSL101 | FillCall ] /LSH101 ( FillCall )

  RUNG pump (* auto, a fill call, and both high-high interlocks clear *)
    AutoSel FillCall /LSHH101 /HHAlarm ( P101_Run )

  RUNG hhdelay (* high-high held 2 s latches the alarm *)
    LSHH101 hhTmr:TON(PT := T#2S) ( S HHAlarm )

  RUNG hhreset (* reset only takes once the switch has cleared *)
    AlmReset /LSHH101 ( R HHAlarm )

  RUNG horn (* a new alarm sounds the horn until acknowledged or reset *)
    [ +HHAlarm | Horn ] /HornAck HHAlarm ( Horn )
END_LD
END_PROGRAM
