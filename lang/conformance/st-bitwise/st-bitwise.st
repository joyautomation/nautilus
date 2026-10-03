PROGRAM BitwiseSt
(* AND, OR, XOR and NOT as operators on INT: bitwise over 64 bits. *)
VAR_EXTERNAL
    X, Y : INT;
    st_AND, st_OR, st_XOR, st_NOT : INT;
END_VAR

st_AND := X AND Y;
st_OR  := X OR Y;
st_XOR := X XOR Y;
st_NOT := NOT X;
END_PROGRAM
