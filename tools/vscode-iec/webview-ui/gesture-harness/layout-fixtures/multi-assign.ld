PROGRAM Repro
VAR_EXTERNAL
    Start : BOOL;
    Stop : BOOL;
    Count : INT;
    Target : INT;
    Big : INT;
    Level : REAL;
    HiSP : REAL;
    Ready : BOOL;
END_VAR
LD
  RUNG r1
    Start { Count := Count + 1; Target := 5; Big := ABS(Big); Level := (Level + HiSP) / 2.0 } Stop ( Ready )
END_LD
END_PROGRAM
