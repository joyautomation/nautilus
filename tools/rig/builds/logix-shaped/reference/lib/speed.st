(* SpeedCalc — the CPT/MOV rung's math. Ladder coils assign BOOL only, so
   the REAL write lives in a block written in ST and called from the rung:
   rung power lands on EN, and the result leaves through the Hz => capture.
   EN false writes 0.0 (the MOV 0 an AB rung puts on the false branch). *)
FUNCTION_BLOCK SpeedCalc
VAR_INPUT
    EN    : BOOL;
    Pct   : REAL; (* speed demand, 0..100 % *)
    MinHz : REAL;
    MaxHz : REAL;
END_VAR
VAR_OUTPUT
    ENO : BOOL;
    Hz  : REAL;
END_VAR
IF EN THEN
    Hz := MinHz + (MaxHz - MinHz) * LIMIT(0.0, Pct, 100.0) / 100.0;
ELSE
    Hz := 0.0;
END_IF;
ENO := EN;
END_FUNCTION_BLOCK
