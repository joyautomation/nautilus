PROGRAM SrSt
(* SR in Structured Text: the reference path. *)
VAR_EXTERNAL
    S    : BOOL;
    R    : BOOL;
    st_Q : BOOL;
END_VAR
VAR
    l : SR;
END_VAR

l(S1 := S, R := R);
st_Q := l.Q1;
END_PROGRAM
