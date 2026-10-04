PROGRAM TrigLogSt
(* Every function applied to the shared inputs; the suite picks the
   inputs whose answers are known. ASIN/ACOS take A so X can be any size. *)
VAR_EXTERNAL
    X, Y, A : REAL;
    st_SQRT, st_LN, st_LOG, st_EXP : REAL;
    st_SIN, st_COS, st_TAN : REAL;
    st_ASIN, st_ACOS, st_ATAN, st_ATAN2, st_EXPT : REAL;
END_VAR

st_SQRT  := SQRT(X);
st_LN    := LN(X);
st_LOG   := LOG(X);
st_EXP   := EXP(X);
st_SIN   := SIN(X);
st_COS   := COS(X);
st_TAN   := TAN(X);
st_ASIN  := ASIN(A);
st_ACOS  := ACOS(A);
st_ATAN  := ATAN(X);
st_ATAN2 := ATAN2(Y, X);
st_EXPT  := EXPT(X, Y);
END_PROGRAM
