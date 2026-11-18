PROGRAM PartSt
VAR_EXTERNAL
    Alarms    : DINT;
    st_Bit3   : BOOL;
    st_Bit3L  : BOOL;   (* the same bit, Logix spelling *)
    st_Byte1  : BYTE;
    st_Packed : WORD;
    Small     : LINT;
    Wide      : LINT;
    st_HiByte : BYTE;
    st_HiWord : WORD;
    st_Dw0    : DWORD;
    st_L0     : LWORD;
    st_Mix    : LINT;
END_VAR
st_Bit3 := Alarms.%X3;
st_Bit3L := Alarms.3;
st_Byte1 := Alarms.%B1;
st_Packed.%X0 := Alarms.%X3;
st_Packed.%X15 := Alarms.%X31;
st_HiByte := Alarms.%B3;
st_HiWord := Alarms.%W1;
st_Dw0 := Alarms.%D0;
st_L0 := Wide.%L0;
st_Mix := 0;
st_Mix.%D1 := Small.%D0;
st_Mix.%W0 := Alarms.%W0;
END_PROGRAM
