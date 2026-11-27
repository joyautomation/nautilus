PROGRAM Main
(* #1305 B1: integer arithmetic where nautilus computes at 64 bits, as the
   Siemens writer writes it. Also the B1 bench program: every result is a
   tag, compared after each scan between the CPU and naut run. *)
VAR_EXTERNAL
    D1, D2 : DINT;        (* inputs near DINT's range *)
    N, M, S, Z : INT;     (* N = 32767, M = -32768, S = 1, Z = 0 *)
    DoFault, DoZero : BOOL;
    rToInt, rDintToInt, rSel, rMax, rLimit : INT;
    rToLint : LINT;
    rToReal : REAL;
    itTop, itBelow, itDown, itVarEnd, itVarStep, itPlclab : DINT;
    exTop, exBelow, exDown, exVarEnd : INT;
    exPlclab : DINT;
    itPlain : DINT;
    exPlain : INT;
    Scans : DINT;
END_VAR
VAR
    i, k : INT;
    j : DINT;
    nb : DINT := 3;
END_VAR
(* conversions of integer arithmetic *)
rToInt := TO_INT(D1 + D2);
rToLint := TO_LINT(D1 + D2);
rDintToInt := DINT_TO_INT(D1 + D2);
rToReal := TO_REAL(D1 + D2);
(* SEL, MAX and LIMIT pass the 64-bit value to the store, which wraps *)
rSel := SEL(TRUE, N, N + 1);
rMax := MAX(N + 1, 0);
rLimit := LIMIT(0, N * 2, 70000);
(* FOR up to the type's last value: 3 passes, ends at 32767 *)
itTop := 0;
FOR i := 32765 TO 32767 DO itTop := itTop + 1; END_FOR;
exTop := i;
(* an end below the type's range: no pass, i unchanged *)
itBelow := 0;
i := 7;
FOR i := 0 TO M - 1 DO itBelow := itBelow + 1; END_FOR;
exBelow := i;
(* down to the type's first value *)
itDown := 0;
FOR i := -32766 TO -32768 BY -1 DO itDown := itDown + 1; END_FOR;
exDown := i;
(* a variable end of the variable's own type, at its last value *)
itVarEnd := 0;
FOR k := 32760 TO N DO itVarEnd := itVarEnd + 1; END_FOR;
exVarEnd := k;
(* a variable step *)
itVarStep := 0;
FOR k := 1 TO 10 BY S DO itVarStep := itVarStep + 1; END_FOR;
(* plclab's shape: an end that is integer arithmetic *)
itPlclab := 0;
FOR j := 0 TO nb - 1 DO itPlclab := itPlclab + 1; END_FOR;
exPlclab := j;
(* a constant FOR, TIA's own, whose body writes its variable: 4 passes *)
itPlain := 0;
FOR k := 1 TO 4 DO itPlain := itPlain + 1; k := 0; END_FOR;
exPlain := k;
Scans := Scans + 1;
(* the guards, only when asked: a step of zero (nautilus faults too) and an
   end past INT's range (nautilus stores 32768.., #1427; the CPU faults) *)
IF DoZero THEN
    FOR k := 1 TO 3 BY Z DO itVarStep := itVarStep + 100; END_FOR;
END_IF;
IF DoFault THEN
    FOR i := 32766 TO N + 5 DO itTop := itTop + 100; END_FOR;
END_IF;
END_PROGRAM
