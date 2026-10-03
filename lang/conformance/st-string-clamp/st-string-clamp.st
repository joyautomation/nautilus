PROGRAM StringClampSt
(* Each string function applied to S with the shared L and P. *)
VAR_EXTERNAL
    S, F : STRING;
    L, P : INT;
    st_Left, st_Right, st_Mid, st_Ins, st_Del, st_Rep, st_Cat : STRING;
    st_Find, st_Len, st_Scans : INT;
END_VAR

st_Left  := LEFT(S, L);
st_Right := RIGHT(S, L);
st_Mid   := MID(S, L, P);
st_Ins   := INSERT(S, 'XY', P);
st_Del   := DELETE(S, L, P);
st_Rep   := REPLACE(S, 'ab', L, P);
st_Find  := FIND(S, F);
st_Len   := LEN(S);
st_Cat   := CONCAT(S, '-', S);
st_Scans := st_Scans + 1;
END_PROGRAM
