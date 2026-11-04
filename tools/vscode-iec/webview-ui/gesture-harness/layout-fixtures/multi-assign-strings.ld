PROGRAM P
VAR_EXTERNAL
    A : BOOL; B : BOOL; C : BOOL; D : BOOL; Q : BOOL; Q2 : BOOL;
    Count : INT; Target : INT; Big : INT; Level : REAL; HiSP : REAL; Msg : STRING;
    VeryLongVariableNameForTesting_Alpha : REAL; VeryLongVariableNameForTesting_Beta : REAL;
END_VAR
LD
  RUNG r1
    A { Msg := 'a;b;c'; Count := 1;; } ( Q )
  RUNG r1b
    B { Msg := 'it$'s;x$$'; Count := 2; Msg := 'q$$;' } ( Q )
  RUNG r2
    A { Count := 1 } { Target := 2; Big := 3 } ( Q2 )
END_LD
END_PROGRAM
