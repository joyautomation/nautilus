PROGRAM XorSt
(* XOR as an operator: logical on BOOL, bitwise on INT. *)
VAR_EXTERNAL
    A, B, C : BOOL;
    X, Y : INT;
    st_AB, st_ABC : BOOL;
    st_XY : INT;
END_VAR

st_AB  := A XOR B;
st_ABC := A XOR B XOR C;
st_XY  := X XOR Y;
END_PROGRAM
