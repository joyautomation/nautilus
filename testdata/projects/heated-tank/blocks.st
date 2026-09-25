(* Reusable function blocks for this project — authored in ST, callable
   from any IEC language: reports.fbd instantiates RateOfChange exactly
   like a built-in TON. main.go composes this file via Libraries; the
   editor, `naut check`, and `naut pull` compose it the same way,
   so online edits round-trip. *)

FUNCTION_BLOCK RateOfChange
(* Rate of change of IN in units/minute, from the previous scan's value.
   Each instance retains its own prev — across scans and online edits. *)
VAR_INPUT
    IN : REAL;
    DT : REAL; (* seconds since last scan *)
END_VAR
VAR_OUTPUT
    OUT : REAL; (* units per minute *)
END_VAR
VAR
    prev   : REAL;
    primed : BOOL;
END_VAR
IF NOT primed THEN
    prev := IN;
    primed := TRUE;
END_IF;
IF DT > 0.0 THEN
    OUT := (IN - prev) / DT * 60.0;
END_IF;
prev := IN;
END_FUNCTION_BLOCK
