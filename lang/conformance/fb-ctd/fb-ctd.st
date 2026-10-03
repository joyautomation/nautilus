PROGRAM CtdSt
(* CTD in Structured Text: the reference path. *)
VAR_EXTERNAL
    CD    : BOOL;
    LOAD  : BOOL;
    PV    : INT;
    st_Q  : BOOL;
    st_CV : INT;
END_VAR
VAR
    c : CTD;
END_VAR

c(CD := CD, LD := LOAD, PV := PV);
st_Q  := c.Q;
st_CV := c.CV;
END_PROGRAM
