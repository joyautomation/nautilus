PROGRAM IntWidthSt
(* One value of each declared integer kind, pushed past its nominal range:
   each store wraps at its type's width, as on a PLC (st_I16 at 32767,
   st_U16 at 0), and the shifts and rotates act on the WORD's 16 bits. *)
VAR_EXTERNAL
    I16 : INT;
    D32 : DINT;
    U16 : UINT;
    W16 : WORD;
    N : INT;
    st_I16 : INT;
    st_D32 : DINT;
    st_U16 : UINT;
    st_W16 : WORD;
    st_Mul : DINT;
    st_Shl, st_Rol, st_Ror : WORD;
    st_Shr : INT;
END_VAR

st_I16 := I16 + 1;
st_D32 := D32 + 1;
st_U16 := U16 - 1;
st_W16 := W16 + 1;
st_Mul := D32 * D32;
st_Shl := SHL(W16, N);
st_Shr := SHR(I16, N);
st_Rol := ROL(W16, N);
st_Ror := ROR(W16, N);
END_PROGRAM
