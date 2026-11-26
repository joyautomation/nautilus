PROGRAM main
VAR
  t1 : TON; t2 : TOF; t3 : TP;
  cu : CTU; cd : CTD; cud : CTUD;
  rt : R_TRIG; ft : F_TRIG;
  sr1 : SR; rs1 : RS;
  a : BOOL; b : BOOL; c : BOOL;
  q1 : BOOL; q2 : BOOL; q3 : BOOL; q4 : BOOL; q5 : BOOL; q6 : BOOL; q7 : BOOL;
  v : LINT; w : LINT; e1 : TIME;
END_VAR
a := Go;
b := NOT Go;
(* timers: all inputs, then PT omitted; output read and binding *)
t1(IN := a, PT := T#2s);
t1(IN := b);
q1 := t1.Q;
e1 := t1.ET;
t2(IN := a, PT := T#1s, Q => q2);
t3(IN := a, PT := T#500ms);
q3 := t3.Q;
(* counters: all inputs, omitted inputs, output reads and bindings *)
cu(CU := a, R := b, PV := 10);
cu(CU := a);
q4 := cu.Q;
v := cu.CV;
cd(CD := a, LD := b, PV := 5);
cd(CD := a);
q5 := cd.Q;
w := cd.CV;
cud(CU := a, CD := b, R := c, LD := c, PV := 7, QU => q6);
cud(CU := b);
q7 := cud.QD;
cu(CU := a, PV := v);
cd(CD := a, LD := b, PV := w);
cud(CU := a, PV := v);
v := cud.CV;
(* edges and latches *)
rt(CLK := a);
ft(CLK := a, Q => c);
Out := rt.Q OR c;
sr1(S1 := a, R := b);
rs1(S := a, R1 := b);
Out := sr1.Q1 AND rs1.Q1;
N := N + 1;
END_PROGRAM
