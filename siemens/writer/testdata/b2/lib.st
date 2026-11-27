FUNCTION Bump : REAL
(* the argument with a side effect: it counts its runs *)
VAR_INPUT x : REAL; END_VAR
VAR_EXTERNAL Bumps : DINT; END_VAR
Bumps := Bumps + 1;
Bump := x;
END_FUNCTION
