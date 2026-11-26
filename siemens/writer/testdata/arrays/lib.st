TYPE
  R : STRUCT
    n : STRING;
    v : REAL := 1.5;
    hist : ARRAY[0..2] OF INT;
  END_STRUCT;
  Mode : (Idle, Run := 5) := Run;
END_TYPE

VAR_GLOBAL CONSTANT
  STAGES : INT := 2 + 1;
END_VAR

FUNCTION Pick : REAL
VAR_INPUT k : INT; t : ARRAY[1..3] OF REAL; END_VAR
Pick := t[k];
END_FUNCTION

FUNCTION_BLOCK Grid
VAR_INPUT k : INT; x : REAL; END_VAR
VAR_OUTPUT y : REAL; END_VAR
VAR tbl : ARRAY[-2..2, 0..3] OF REAL; END_VAR
tbl[k, k + 1] := x;
y := tbl[k, k + 1];
END_FUNCTION_BLOCK

FUNCTION_BLOCK Outer
VAR_INPUT k : INT; END_VAR
VAR_OUTPUT y : REAL; END_VAR
VAR g : Grid; END_VAR
g(k := k, x := 2.0, y => y);
END_FUNCTION_BLOCK
