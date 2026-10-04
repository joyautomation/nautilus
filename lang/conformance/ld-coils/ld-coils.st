PROGRAM CoilsSt
(* The reference semantics the ladder coils and contacts must match. *)
VAR_EXTERNAL
    A, B, SetIn, RstIn, CLK : BOOL;
    N : INT;
    st_Latch, st_P, st_N, st_RiseC, st_FallC, st_CntDone, st_NotZero, st_Br : BOOL;
    st_Cnt : INT;
END_VAR
VAR
    rp, fp, rc, fc : BOOL;
    rt : R_TRIG;
    ft : F_TRIG;
    rtc : R_TRIG;
    ftc : F_TRIG;
    cnt : CTU;
    brt : TON;
END_VAR

(* S then R coil: R is later in the scan, so it wins a tie *)
IF SetIn THEN st_Latch := TRUE; END_IF;
IF RstIn THEN st_Latch := FALSE; END_IF;

(* P and N coils *)
rt(CLK := A);  st_P := rt.Q;
ft(CLK := A);  st_N := ft.Q;

(* + and - contacts *)
rtc(CLK := B); st_RiseC := rtc.Q;
ftc(CLK := B); st_FallC := ftc.Q;

(* FB-only rung, read elsewhere *)
cnt(CU := CLK, R := FALSE, PV := 3);
st_Cnt := cnt.CV;
st_CntDone := cnt.Q;

(* negated function contact *)
st_NotZero := NOT (N = 0);

(* nested branch with a function block in a leg *)
brt(IN := B OR CLK, PT := T#3ms);
st_Br := A OR brt.Q;
END_PROGRAM
