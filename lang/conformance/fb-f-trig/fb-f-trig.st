PROGRAM FTrigSt
(* F_TRIG in Structured Text: the reference path. *)
VAR_EXTERNAL
    CLK  : BOOL;
    st_Q : BOOL;
END_VAR
VAR
    t : F_TRIG;
END_VAR

t(CLK := CLK);
st_Q := t.Q;
END_PROGRAM
