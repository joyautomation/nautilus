PROGRAM ConvSt
(* Every conversion applied to the shared inputs, each result to its own
   st_ tag. A STRING_TO_* given text that does not parse faults the scan,
   so the suite only feeds those valid text; the fault itself is pinned in
   faults_test.go. *)
VAR_EXTERNAL
    R : REAL;
    I : INT;
    B : BOOL;
    T : TIME;
    SI, SR, SB : STRING;
    st_INT_TO_REAL, st_TIME_TO_REAL, st_BOOL_TO_REAL, st_STRING_TO_REAL : REAL;
    st_REAL_TO_INT, st_BOOL_TO_INT, st_TIME_TO_INT, st_STRING_TO_INT, st_TRUNC : INT;
    st_INT_TO_BOOL, st_REAL_TO_BOOL, st_STRING_TO_BOOL : BOOL;
    st_INT_TO_TIME, st_REAL_TO_TIME : TIME;
    st_INT_TO_STRING, st_REAL_TO_STRING, st_BOOL_TO_STRING, st_TIME_TO_STRING : STRING;
END_VAR

st_INT_TO_REAL    := INT_TO_REAL(I);
st_REAL_TO_INT    := REAL_TO_INT(R);
st_BOOL_TO_INT    := BOOL_TO_INT(B);
st_INT_TO_BOOL    := INT_TO_BOOL(I);
st_INT_TO_TIME    := INT_TO_TIME(I);
st_TIME_TO_INT    := TIME_TO_INT(T);
st_REAL_TO_TIME   := REAL_TO_TIME(R);
st_TIME_TO_REAL   := TIME_TO_REAL(T);
st_BOOL_TO_REAL   := BOOL_TO_REAL(B);
st_REAL_TO_BOOL   := REAL_TO_BOOL(R);
st_INT_TO_STRING  := INT_TO_STRING(I);
st_REAL_TO_STRING := REAL_TO_STRING(R);
st_BOOL_TO_STRING := BOOL_TO_STRING(B);
st_TIME_TO_STRING := TIME_TO_STRING(T);
st_STRING_TO_INT  := STRING_TO_INT(SI);
st_STRING_TO_REAL := STRING_TO_REAL(SR);
st_STRING_TO_BOOL := STRING_TO_BOOL(SB);
st_TRUNC          := TRUNC(R);
END_PROGRAM
