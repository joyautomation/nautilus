PROGRAM Slow
(* the other byte of the shared tag, at another rate *)
VAR_EXTERNAL
    Sh : DWORD;
    SlowN : DINT;
END_VAR
SlowN := SlowN + 1;
Sh.%B1 := DINT_TO_BYTE(SlowN MOD 256);
END_PROGRAM
