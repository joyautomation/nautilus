PROGRAM P
VAR_EXTERNAL
    A : BOOL; B : BOOL; C : BOOL; D : BOOL; Q : BOOL; Q2 : BOOL;
    Count : INT; Target : INT; Big : INT; Level : REAL; HiSP : REAL; Msg : STRING;
    VeryLongVariableNameForTesting_Alpha : REAL; VeryLongVariableNameForTesting_Beta : REAL;
END_VAR
LD
  RUNG r1
    A { Count := 1; Target := 2; Big := 3; Count := Count + 1; Target := Target + 2; Big := Big + 3; Level := 1.0; HiSP := 2.0; Count := 9; Target := 10 } B ( Q )
  RUNG r2
    C ( Q2 )
END_LD
END_PROGRAM
