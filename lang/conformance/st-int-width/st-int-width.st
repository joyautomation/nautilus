PROGRAM IntWidthSt
(* One value of each declared integer kind, pushed past its nominal range.
   If widths were honoured, st_I16 would wrap at 32767 and st_U16 at 0. *)
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
    st_Shl, st_Shr, st_Rol, st_Ror : INT;
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
