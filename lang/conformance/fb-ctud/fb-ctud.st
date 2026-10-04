PROGRAM CtudSt
(* CTUD in Structured Text: the reference path. *)
VAR_EXTERNAL
    CU     : BOOL;
    CD     : BOOL;
    R      : BOOL;
    LOAD   : BOOL;
    PV     : INT;
    st_QU  : BOOL;
    st_QD  : BOOL;
    st_CV  : INT;
END_VAR
VAR
    c : CTUD;
END_VAR

c(CU := CU, CD := CD, R := R, LD := LOAD, PV := PV);
st_QU := c.QU;
st_QD := c.QD;
st_CV := c.CV;
END_PROGRAM
