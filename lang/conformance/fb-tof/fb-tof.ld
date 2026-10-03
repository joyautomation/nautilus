PROGRAM TofLd
(* TOF in ladder: rung power drives IN, Q continues to the coil, ET is
   captured with the standard's output binding. *)
VAR_EXTERNAL
    IN    : BOOL;
    PT    : TIME;
    ld_Q  : BOOL;
    ld_ET : TIME;
END_VAR
VAR
    t : TOF;
END_VAR
LD
  RUNG tof
    IN t:TOF(PT := PT, ET => ld_ET) ( ld_Q )
END_LD
END_PROGRAM
