PROGRAM TpLd
(* TP in ladder: rung power drives IN, Q continues to the coil, ET is
   captured with the standard's output binding. *)
VAR_EXTERNAL
    IN    : BOOL;
    PT    : TIME;
    ld_Q  : BOOL;
    ld_ET : TIME;
END_VAR
VAR
    t : TP;
END_VAR
LD
  RUNG tp
    IN t:TP(PT := PT, ET => ld_ET) ( ld_Q )
END_LD
END_PROGRAM
