PROGRAM EnEnoSt
(* EN/ENO in Structured Text: the formal call carries EN and ENO. ST has
   no DIV function, so its division is gated the way an ST programmer
   writes it — the error flag is the zero divisor. *)
VAR_EXTERNAL
    EN      : BOOL;
    X       : REAL;
    D       : REAL;
    CU      : BOOL;
    st_OUT  : REAL;
    st_ENO  : BOOL;
    st_CV   : INT;
    st_CENO : BOOL;
    st_Q    : REAL;
    st_QENO : BOOL;
END_VAR
VAR
    c : CTU;
END_VAR

st_OUT := LIMIT(EN := EN, MN := 0.0, IN := X, MX := 10.0, ENO => st_ENO);
c(EN := EN, CU := CU, R := FALSE, PV := 100, CV => st_CV, ENO => st_CENO);
IF EN THEN
    st_QENO := D <> 0.0;
    st_Q := X / D;
ELSE
    st_QENO := FALSE;
END_IF;
END_PROGRAM
