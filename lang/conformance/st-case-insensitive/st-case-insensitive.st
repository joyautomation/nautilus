PROGRAM CaseSt
(* Level in three casings; ScaleAnalog, AboveHalf, CTU, the counter
   instance and its pins all cased differently from their declarations. *)
VAR_EXTERNAL
    LEVEL     : REAL;
    start     : BOOL;
    ST_SCALED : REAL;
    st_half   : BOOL;
    St_Starts : INT;
END_VAR
VAR
    StartCount : ctu;
END_VAR
    st_scaled := scaleanalog(level, 0.0, 120.0);
    ST_HALF := ABOVEHALF(Level);
    startcount(cu := START, r := FALSE, pv := 1000);
    st_starts := STARTCOUNT.cv;
END_PROGRAM
