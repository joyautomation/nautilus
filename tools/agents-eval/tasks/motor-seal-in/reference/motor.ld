PROGRAM Motor
(* Motor starter: three-wire start/stop with an overload fault latch. *)
LD
  RUNG olfault (* an overload trip latches the fault lamp *)
    OL_Trip ( S Motor_Fault )

  RUNG faultreset (* reset only takes once the overload has cooled *)
    ResetPB /OL_Trip ( R Motor_Fault )

  RUNG run (* start seals in; stop (NC) and a latched fault drop it *)
    [ StartPB | Motor_Run ] StopPB /OL_Trip /Motor_Fault ( Motor_Run )
END_LD
END_PROGRAM
