FUNCTION ScaleAnalog : REAL
(* FC105 SCALE, S7-1500 style: a raw analog value (0..27648 nominal) to
   engineering units, clamped to EngLo..EngHi. *)
VAR_INPUT
    Raw   : INT;
    EngLo : REAL;
    EngHi : REAL;
END_VAR
VAR_TEMP
    frac : REAL;
END_VAR
frac := INT_TO_REAL(Raw) / 27648.0;
ScaleAnalog := LIMIT(EngLo, EngLo + frac * (EngHi - EngLo), EngHi);
END_FUNCTION
