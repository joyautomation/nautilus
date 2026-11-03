(* library file: types used by the typed project *)
TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE
TYPE Drive : STRUCT
    Speed : REAL;
    Amps  : REAL;
  END_STRUCT;
END_TYPE
TYPE Motor : STRUCT
    Running : BOOL;
    Fault   : BOOL;
    Starts  : DINT;
    Drive   : Drive;
    State   : Mode;
  END_STRUCT;
END_TYPE
TYPE DoseRecipe : STRUCT
    TargetL    : REAL;
    SettleTime : TIME;
    Steps      : ARRAY[0..2] OF TIME;
  END_STRUCT;
END_TYPE
