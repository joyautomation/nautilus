PROGRAM Edges
(* Rising and falling edge contacts, at the head of a rung and after another
   contact. A one-scan pulse cannot be seen over EtherNet/IP, so every edge
   latches what it saw and Clear releases the latches. *)
VAR_EXTERNAL
    Btn       : BOOL;
    Arm       : BOOL;
    Clear     : BOOL;
    RoseOnce  : BOOL;
    FellOnce  : BOOL;
    ArmedRose : BOOL;
END_VAR
LD
  RUNG rose (* rising edge at the head of the rung: ONS inline *)
    +Btn ( S RoseOnce )

  RUNG fell (* falling edge: an OSF helper rung *)
    -Btn ( S FellOnce )

  RUNG armed (* the edge is Btn's own, not the rung's: Arm toggling while Btn is high must not latch *)
    Arm +Btn ( S ArmedRose )

  RUNG clear
    Clear ( R RoseOnce ) ( R FellOnce ) ( R ArmedRose )
END_LD
END_PROGRAM
