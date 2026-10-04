PROGRAM MainProgram
(* Line.L5X's Receive routine: ready while the tank has headroom; a
   request against a ready, unfaulted line starts a 2 s settling timer and
   Line_Accept follows; a high level cuts the accept off; a fault raises
   the status alarm. *)
VAR_EXTERNAL
    Line_Request   : BOOL;
    Line_Fault     : BOOL;
    Line_TankLevel : REAL;
    Line_BatchId   : DINT;
    Line_Ready     : BOOL;
    Line_Accept    : BOOL;
    Line_Status    : LineStatus;
END_VAR
VAR
    AcceptTmr : TON;
END_VAR
LD
  RUNG ready
    LT(Line_TankLevel, 80.0) ( Line_Ready )

  RUNG settle
    Line_Request Line_Ready /Line_Fault AcceptTmr:TON(PT := T#2S)

  RUNG accept
    AcceptTmr.Q ( Line_Accept )

  RUNG alarm
    Line_Fault ( Line_Status.Alarm )

  RUNG cutoff (* the second revision's high-level interlock *)
    GE(Line_TankLevel, 95.0) ( R Line_Accept )
END_LD
END_PROGRAM
