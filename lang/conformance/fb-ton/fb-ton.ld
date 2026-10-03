PROGRAM TonLd
(* TON in ladder: rung power drives IN, Q continues to the coil, ET is
   captured with the standard's output binding. *)
VAR_EXTERNAL
    IN    : BOOL;
    PT    : TIME;
    ld_Q  : BOOL;
    ld_ET : TIME;
END_VAR
VAR
    t : TON;
END_VAR
LD
  RUNG ton
    IN t:TON(PT := PT, ET => ld_ET) ( ld_Q )
END_LD
END_PROGRAM
