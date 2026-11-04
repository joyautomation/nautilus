PROGRAM P
VAR_EXTERNAL
    A : BOOL; B : BOOL; C : BOOL; D : BOOL; Q : BOOL; Q2 : BOOL;
    Count : INT; Target : INT; Big : INT; Level : REAL; HiSP : REAL; Msg : STRING;
    VeryLongVariableNameForTesting_Alpha : REAL; VeryLongVariableNameForTesting_Beta : REAL;
END_VAR
LD
  RUNG r1
    [ A { Count := Count + 1; Target := 5; Big := ABS(Big); Level := (Level + HiSP) / 2.0 } | B { Count := 0; Target := 0 } | C ] D ( Q )
  RUNG r2
    A [ B | C ] { Count := 1; Target := 2; Big := 3 } ( Q2 )
END_LD
END_PROGRAM
