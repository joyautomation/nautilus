PROGRAM Main
(* #1305 B2: integer ABS, EN/ENO, ATAN2, as the Siemens writer writes them.
   Also the B2 bench program: results are tags, compared with naut run. *)
VAR_EXTERNAL
    A16 : INT;  A32 : DINT;  A64 : LINT;          (* the types' least values *)
    rA16 : INT; rA32 : DINT; rA64 : LINT; rAbs5 : INT;
    En, Cu : BOOL;
    X : REAL;
    Bumps : DINT;                                   (* counts Bump's runs *)
    rLim : REAL; eno1 : BOOL;
    cv : INT; eno2 : BOOL;
    Sp : ARRAY[0..48] OF LREAL;                     (* ATAN2 at the special values *)
    Gr : ARRAY[0..143] OF REAL;                     (* ATAN2 on a REAL grid *)
    Scans : DINT;
END_VAR
VAR
    c : CTU;
    sv : ARRAY[0..6] OF LREAL;
    gv : ARRAY[0..11] OF REAL;
    i, j : INT;
    inf, nz : LREAL;
END_VAR
(* integer ABS: 64 bits, wrapped by the store *)
rA16 := ABS(A16);
rA32 := ABS(A32);
rA64 := ABS(A64);
rAbs5 := ABS(-5);
(* EN/ENO: while En is FALSE the call does not run, its argument Bump is
   not evaluated, the target and the bindings keep their values *)
rLim := LIMIT(EN := En, MN := 0.0, IN := Bump(x := X), MX := 10.0, ENO => eno1);
c(EN := En, CU := Cu, R := FALSE, PV := 100, CV => cv, ENO => eno2);
(* ATAN2: the special values, made at run time (inf, NaN, -0) *)
inf := 1.0E300 * 1.0E300;
nz := 0.0 * -1.0;
sv[0] := nz; sv[1] := 0.0; sv[2] := 1.0; sv[3] := -1.0;
sv[4] := inf; sv[5] := -inf; sv[6] := inf - inf;
FOR i := 0 TO 6 DO
    FOR j := 0 TO 6 DO
        Sp[i * 7 + j] := ATAN2(sv[i], sv[j]);
    END_FOR;
END_FOR;
gv[0] := 0.5; gv[1] := -0.5; gv[2] := 3.0; gv[3] := -3.0;
gv[4] := 0.001; gv[5] := -0.001; gv[6] := 100.0; gv[7] := -100.0;
gv[8] := 0.7071; gv[9] := -2.5; gv[10] := 1000000.0; gv[11] := -1000000.0;
FOR i := 0 TO 11 DO
    FOR j := 0 TO 11 DO
        Gr[i * 12 + j] := ATAN2(gv[i], gv[j]);
    END_FOR;
END_FOR;
Scans := Scans + 1;
END_PROGRAM
