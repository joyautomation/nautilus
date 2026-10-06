PROGRAM PartLd
VAR_EXTERNAL
    Alarms    : DINT;
    ld_Bit3   : BOOL;
    ld_Packed : WORD;
END_VAR
LD
  RUNG bit3
    Alarms.%X3 ( ld_Bit3 )
  RUNG pack0
    Alarms.%X3 ( ld_Packed.%X0 )
  RUNG pack15
    Alarms.%X31 ( ld_Packed.%X15 )
END_LD
END_PROGRAM
