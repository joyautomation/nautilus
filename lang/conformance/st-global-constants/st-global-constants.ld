PROGRAM GConstLd
(* The project constants as a compare block's operand and a timer preset. *)
VAR_EXTERNAL
    Level   : REAL;
    Run     : BOOL;
    ld_High : BOOL;
    ld_Done : BOOL;
END_VAR
VAR
    t : TON;
END_VAR
LD
  RUNG high
    GT(Level, HI_LIMIT) ( ld_High )
  RUNG delay
    Run t:TON(PT := T_DELAY) ( ld_Done )
END_LD
END_PROGRAM
