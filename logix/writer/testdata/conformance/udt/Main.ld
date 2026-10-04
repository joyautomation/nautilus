PROGRAM UDT
(* Structure tags: a coil on a member, a contact on a nested member, a
   compare on a numeric member. The member paths are the same text in
   nautilus and in Logix. *)
VAR_EXTERNAL
    Line_Fault  : BOOL;
    SpeedCmd    : REAL;
    Line_Status : LineStatus;
    P101        : Pump;
    Auto        : BOOL;
    Nested      : BOOL;
END_VAR
LD
  RUNG alarm (* a coil on a member *)
    Line_Fault ( Line_Status.Alarm )

  RUNG auto (* a compare on a numeric member *)
    EQ(Line_Status.Mode, 1) /Line_Status.Alarm ( Auto )

  RUNG run (* a coil on a nested structure's member *)
    GT(SpeedCmd, 50.0) ( P101.Run )

  RUNG nested (* a contact on a member two levels down *)
    P101.Status.Alarm P101.Run ( Nested )
END_LD
END_PROGRAM
