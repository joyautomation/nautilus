PROGRAM Main
VAR CONSTANT N : INT := 4; END_VAR
VAR_EXTERNAL sel : INT; tbl : ARRAY[1..3] OF REAL; END_VAR
VAR
  a : ARRAY[1..N] OF DINT;
  b : ARRAY[-2..2, 0..3] OF INT;
  r : ARRAY[1..STAGES] OF R;
  m : ARRAY[0..1] OF Mode;
  s : ARRAY[1..2] OF STRING;
  i, j : INT;
  sum : DINT;
  x, y : REAL;
  act : R;
  inst : Grid;
  o : Outer;
  outs : ARRAY[0..2] OF REAL;
END_VAR
FOR i := 1 TO N DO a[i] := i; sum := sum + a[i]; END_FOR;
a[2] := 7;
b[i, j + 1] := b[-2, 0];
act := r[sel];
r[1].hist[j] := i;
x := tbl[i] + Pick(k := j, t := tbl) * 2.0;
inst(k := sel, x := tbl[j], y => outs[i]);
o(k := j, y => y);
IF x > 0.0 THEN y := 1.0; ELSIF tbl[j] > 1.0 THEN y := 2.0; ELSE y := 3.0; END_IF;
WHILE a[i] > 0 DO i := i + 1; END_WHILE;
outs[i] := tbl[j] + Pick(k := i, t := tbl);
s[1] := 'x';
m[0] := Idle;
END_PROGRAM
