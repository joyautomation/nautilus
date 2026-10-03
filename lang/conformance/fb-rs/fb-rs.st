PROGRAM RsSt
(* RS in Structured Text: the reference path. *)
VAR_EXTERNAL
    S    : BOOL;
    R    : BOOL;
    st_Q : BOOL;
END_VAR
VAR
    l : RS;
END_VAR

l(S := S, R1 := R);
st_Q := l.Q1;
END_PROGRAM
