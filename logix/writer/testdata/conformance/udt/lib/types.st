(* The structure types the udt conformance project uses: a flat one like
   batch-skid's LineStatus, and one that nests it. *)
TYPE
  LineStatus : STRUCT
    Mode  : DINT;
    Alarm : BOOL;
  END_STRUCT;
  Pump : STRUCT
    Run    : BOOL;
    Speed  : REAL;
    Status : LineStatus;
  END_STRUCT;
END_TYPE
