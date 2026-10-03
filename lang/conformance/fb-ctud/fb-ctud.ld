PROGRAM CtudLd
(* CTUD in ladder: rung power drives CU, QU continues to the coil, the
   other pins are bound by name. *)
VAR_EXTERNAL
    CU     : BOOL;
    CD     : BOOL;
    R      : BOOL;
    LOAD   : BOOL;
    PV     : INT;
    ld_QU  : BOOL;
    ld_QD  : BOOL;
    ld_CV  : INT;
END_VAR
VAR
    c : CTUD;
END_VAR
LD
  RUNG ctud
    CU c:CTUD(CD := CD, R := R, LD := LOAD, PV := PV, QD => ld_QD, CV => ld_CV) ( ld_QU )
END_LD
END_PROGRAM
