PROGRAM CtdLd
(* CTD in ladder: rung power drives CD, Q continues to the coil, CV is
   captured with the standard's output binding. *)
VAR_EXTERNAL
    CD    : BOOL;
    LOAD  : BOOL;
    PV    : INT;
    ld_Q  : BOOL;
    ld_CV : INT;
END_VAR
VAR
    c : CTD;
END_VAR
LD
  RUNG ctd
    CD c:CTD(LD := LOAD, PV := PV, CV => ld_CV) ( ld_Q )
END_LD
END_PROGRAM
