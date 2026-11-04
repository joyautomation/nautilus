PROGRAM P
VAR_EXTERNAL
    A : BOOL; B : BOOL; C : BOOL; D : BOOL; Q : BOOL; Q2 : BOOL;
    Count : INT; Target : INT; Big : INT; Level : REAL; HiSP : REAL; Msg : STRING;
    VeryLongVariableNameForTesting_Alpha : REAL; VeryLongVariableNameForTesting_Beta : REAL;
END_VAR
LD
  RUNG r1
    A { VeryLongVariableNameForTesting_Alpha := VeryLongVariableNameForTesting_Beta * 2.0 + Level } B ( Q )
END_LD
END_PROGRAM
