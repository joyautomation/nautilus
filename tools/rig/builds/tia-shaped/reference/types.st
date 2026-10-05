(* PLC data types (TIA: "PLC data types"). DoseRecipe is the recipe DB's
   type: one global tag of it per dosing line. *)
TYPE
  DoseRecipe : STRUCT
    TargetL    : REAL;  (* amount to dose, L *)
    FlowSP     : REAL;  (* flow setpoint for FCV-101, L/min *)
    SettleSec  : REAL;  (* wait after the valve shuts before counting the dose, s *)
    DoneCount  : INT;   (* doses completed with this recipe *)
    LastDosedL : REAL;  (* litres delivered by the last dose *)
  END_STRUCT;
END_TYPE
