PROGRAM SrLd
(* SR in ladder: rung power drives S1, R is passed by name, Q1 continues to the coil. *)
VAR_EXTERNAL
    S    : BOOL;
    R    : BOOL;
    ld_Q : BOOL;
END_VAR
VAR
    l : SR;
END_VAR
LD
  RUNG latch
    S l:SR(R := R) ( ld_Q )
END_LD
END_PROGRAM
