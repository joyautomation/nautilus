PROGRAM Main
(* Every conversion class the Logix writer writes (logix/writer/convert.go),
   one result tag per statement, driven by the input tags x*. The REAL
   inputs are exact in float32 (3.75, not 3.7): the controller's REAL is 32
   bits and nautilus's 64, so an inexact input would differ in its last
   digits on every path, conversion or not. *)
VAR_EXTERNAL
    xd : DINT;
    xa : DINT;
    xi : INT;
    xs : SINT;
    xr : REAL;
    xb : BOOL;
    xt : TIME;
    xk : DINT;
    rI1 : INT;
    rS1 : SINT;
    rS2 : SINT;
    rI2 : INT;
    rD1 : DINT;
    rD2 : DINT;
    rD3 : DINT;
    rD4 : DINT;
    rD5 : DINT;
    rI3 : INT;
    rD6 : DINT;
    rI4 : INT;
    rD7 : DINT;
    rI5 : INT;
    rT1 : TIME;
    rR1 : REAL;
    rR2 : REAL;
    rR3 : REAL;
    rR4 : REAL;
    rL1 : LREAL;
    rL2 : LREAL;
    rR5 : REAL;
    rB1 : BOOL;
    rB2 : BOOL;
    rB3 : BOOL;
    rB4 : BOOL;
    rD8 : DINT;
    rR6 : REAL;
    rD9 : DINT;
    rB5 : BOOL;
    rR7 : REAL;
    rD10 : DINT;
    rI6 : INT;
    rS3 : SINT;
    rT2 : TIME;
    rD11 : DINT;
    rR8 : REAL;
    rR10 : REAL;
    rR11 : REAL;
    rD12 : DINT;
END_VAR
VAR
    arr : ARRAY [0..3] OF INT;
    pr1 : PassR;
    pr2 : PassR;
    pd1 : PassD;
END_VAR
arr[0] := 10;
arr[1] := 20;
arr[2] := 30;
arr[3] := 40;
(* integer narrowing: the 16- and 8-bit wrap in integer arithmetic *)
rI1 := DINT_TO_INT(xd);
rS1 := DINT_TO_SINT(xd);
rS2 := INT_TO_SINT(xi);
rI2 := DINT_TO_INT(70000);
rD1 := DINT_TO_INT(xa + xd) * 2;
(* an argument wider than the source is read as the source first *)
rD2 := INT_TO_DINT(xd);
rD3 := INT_TO_DINT(xa + xd);
(* widening, TIME as DINT milliseconds *)
rD4 := INT_TO_DINT(xi);
rD5 := SINT_TO_DINT(xs) + 1;
rI3 := SINT_TO_INT(xs);
rD6 := TO_DINT(xi);
rI4 := TO_INT(xd);
rD7 := TIME_TO_DINT(xt);
rI5 := TIME_TO_INT(xt);
rT1 := DINT_TO_TIME(xd);
(* integer to REAL and LREAL *)
rR1 := DINT_TO_REAL(xd);
rR2 := xr + INT_TO_REAL(xi);
rR3 := DINT_TO_REAL(xa) / 2;
rR4 := DINT_TO_REAL(xa) / 2.0;
rL1 := DINT_TO_LREAL(xd);
rL2 := REAL_TO_LREAL(xr);
rR5 := LREAL_TO_REAL(rL2);
IF INT_TO_REAL(xi) > xr THEN
    rB1 := TRUE;
ELSE
    rB1 := FALSE;
END_IF;
(* to and from BOOL *)
rB2 := DINT_TO_BOOL(xd);
rB3 := INT_TO_BOOL(xd);
rB4 := REAL_TO_BOOL(xr);
rD8 := BOOL_TO_DINT(xb);
rR6 := BOOL_TO_REAL(xb);
(* REAL to integer: TRUNC, and a store that rounds halves to even *)
rD9 := TRUNC_DINT(xr);
rD10 := REAL_TO_DINT(xr);
rI6 := REAL_TO_INT(xr);
rS3 := REAL_TO_SINT(xr);
rT2 := REAL_TO_TIME(xr);
(* a comparison and an index are computations of their own *)
IF DINT_TO_INT(xd) > 5 AND xr > 1.5 THEN
    rB5 := TRUE;
ELSE
    rB5 := FALSE;
END_IF;
rR7 := xr + arr[DINT_TO_SINT(xk)];
(* an integer / truncates; its quotient reaches a REAL through a DINT tag *)
rD11 := xa / 2;
rR8 := DINT_TO_REAL(rD11);
(* a user block's input is a store into the parameter *)
pr1(x := DINT_TO_REAL(xa), y => rR10); (* IEC: a DINT into a REAL input is explicit, #782 *)
pr2(x := DINT_TO_REAL(xd), y => rR11);
pd1(x := REAL_TO_DINT(xr), y => rD12);
END_PROGRAM
