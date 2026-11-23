PROGRAM Main
(* Writer coverage PR A (#789): one of each construct it added. *)
VAR
    a, b, n : INT;
    u : UINT;
    d : DINT;
    w, w2 : WORD;
    l : LINT;
    r, r2 : REAL;
    lr : LREAL;
    q, q2 : BOOL;
    latch : SR;
    unlatch : RS;
END_VAR

(* bitwise: same width on the bit string, else at 64 bits *)
a := a AND b;
a := a OR 16#0F;
a := a XOR -2;
w := w AND w2;
w := w OR 16#FF00;
d := a AND d;
a := NOT a;
w := NOT w;
d := NOT u;
q := NOT u > 5;
w := w + 1;
q := w.3;
w.4 := q;

(* divisions nautilus answers 0 for a zero divisor *)
a := a / b;
a := a MOD b;
r := r / r2;
lr := lr / lr;
r := r / a;

(* the function table *)
l := TRUNC(r);
d := TRUNC_DINT(r);
d := TO_DINT(a);
r := TO_REAL(a);
a := MAX(a, b, n);
a := MIN(IN2 := a, IN1 := b);
a := MUX(n, a, b, 7);
a := LIMIT(MX := 10, IN := a, MN := 0);
w := SHL(w, n);
a := SHR(a, n);
w := ROL(w, 3);
u := ROR(u, u);

(* SR and RS, inline *)
latch(S1 := Start, R := Reset, Q1 => q);
unlatch(S := Start, R1 := Reset);
q2 := unlatch.Q1;
Running := latch.Q1 AND NOT unlatch.Q1;
END_PROGRAM
