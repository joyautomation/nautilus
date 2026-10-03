PROGRAM DivZeroSt
(* Division and remainder, plus a counter written after them: if a divide
   by zero ended the scan early the counter would stop. *)
VAR_EXTERNAL
    N, D : INT;
    RN, RD : REAL;
    st_Q, st_M, st_Scans : INT;
    st_RQ : REAL;
END_VAR

st_Q  := N / D;
st_M  := N MOD D;
st_RQ := RN / RD;
st_Scans := st_Scans + 1;
END_PROGRAM
