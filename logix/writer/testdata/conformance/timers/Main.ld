PROGRAM Timers
(* TON and TOF against their Logix counterparts: the delay, the reset
   while timing, and the done bit's life after the input drops. *)
VAR_EXTERNAL
    In         : BOOL;
    OnDelayed  : BOOL;
    Timing     : BOOL;
    OffDelayed : BOOL;
END_VAR
VAR
    t_on  : TON;
    t_off : TOF;
END_VAR
LD
  RUNG ondelay (* 2 s on-delay; power continues from the done bit *)
    In t_on:TON(PT := T#2S) ( OnDelayed )

  RUNG timing (* true while the on-delay is running *)
    In /t_on.Q ( Timing )

  RUNG offdelay (* 2 s off-delay; a TOF ends its rung *)
    In t_off:TOF(PT := T#2S)

  RUNG offout
    t_off.Q ( OffDelayed )
END_LD
END_PROGRAM
