PROGRAM TempLd
(* The same instance called on two rungs. *)
VAR_EXTERNAL
    ld_Fresh1 : INT;
    ld_Fresh2 : INT;
    ld_Total  : INT;
END_VAR
VAR
    s : Scratch;
END_VAR
LD
  RUNG first
    TRUE s:Scratch(Add := 1, Fresh => ld_Fresh1)
  RUNG second
    TRUE s:Scratch(Add := 2, Fresh => ld_Fresh2, Total => ld_Total)
END_LD
END_PROGRAM
