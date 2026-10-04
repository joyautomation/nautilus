PROGRAM TpSt
(* TP in Structured Text: the reference path. *)
VAR_EXTERNAL
    IN    : BOOL;
    PT    : TIME;
    st_Q  : BOOL;
    st_ET : TIME;
END_VAR
VAR
    t : TP;
END_VAR

t(IN := IN, PT := PT);
st_Q  := t.Q;
st_ET := t.ET;
END_PROGRAM
