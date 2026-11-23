(* A faster task: ramps over its dt-tag, an LREAL. *)
PROGRAM Fast
VAR_EXTERNAL
  FastDt : LREAL;
  Ramp : LREAL;
END_VAR
Ramp := Ramp + 2.0 * FastDt;
END_PROGRAM
