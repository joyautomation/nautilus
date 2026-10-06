PROGRAM CaseLd
VAR_EXTERNAL
    level     : REAL;
    START     : BOOL;
    LD_HALF   : BOOL;
    ld_starts : INT;
END_VAR
VAR
    Counter : CTU;
END_VAR
LD
  RUNG half
    aboveHALF(LEVEL) ( ld_half )
  RUNG count
    start COUNTER:ctu(R := FALSE, PV := 1000, cv => LD_STARTS)
END_LD
END_PROGRAM
