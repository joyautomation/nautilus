PROGRAM RsLd
(* RS in ladder: rung power drives S, R1 is passed by name, Q1 continues to the coil. *)
VAR_EXTERNAL
    S    : BOOL;
    R    : BOOL;
    ld_Q : BOOL;
END_VAR
VAR
    l : RS;
END_VAR
LD
  RUNG latch
    S l:RS(R1 := R) ( ld_Q )
END_LD
END_PROGRAM
