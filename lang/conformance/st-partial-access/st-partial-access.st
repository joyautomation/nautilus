PROGRAM PartSt
VAR_EXTERNAL
    Alarms    : DINT;
    st_Bit3   : BOOL;
    st_Bit3L  : BOOL;   (* the same bit, Logix spelling *)
    st_Byte1  : INT;
    st_Packed : WORD;
END_VAR
st_Bit3 := Alarms.%X3;
st_Bit3L := Alarms.3;
st_Byte1 := Alarms.%B1;
st_Packed.%X0 := Alarms.%X3;
st_Packed.%X15 := Alarms.%X31;
END_PROGRAM
