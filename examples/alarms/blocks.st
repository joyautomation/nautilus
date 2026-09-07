(* A root .st file with no PROGRAM is a library: it is in scope for every
   task's compile, so `type: AnalogInput` in the manifest resolves against
   it — and so an alarm rule can say `match: {type: AnalogInput, member:
   HH}` and mean this. *)

TYPE
  AnalogInput : STRUCT
    PV : REAL;   (* the measurement *)
    HH : BOOL;   (* the four limit bits the alarm rules claim *)
    H  : BOOL;
    L  : BOOL;
    LL : BOOL;
  END_STRUCT;
END_TYPE
