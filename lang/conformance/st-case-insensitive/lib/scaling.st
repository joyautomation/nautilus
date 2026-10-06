(* Library POUs, each referenced elsewhere in a casing other than the
   declaration's. Inside their own bodies too: the return variable and the
   inputs are read and written in other casings. *)
FUNCTION ScaleAnalog : REAL
VAR_INPUT
    Raw : REAL;
    Lo  : REAL;
    Hi  : REAL;
END_VAR
    SCALEANALOG := lo + RAW * (HI - Lo) / 100.0;
END_FUNCTION

FUNCTION AboveHalf : BOOL
VAR_INPUT
    X : REAL;
END_VAR
    abovehalf := x > 50.0;
END_FUNCTION
