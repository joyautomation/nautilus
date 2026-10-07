PROGRAM FanoutLd
VAR_EXTERNAL
    ld_Lamp, ld_Horn, ld_Done, ld_Pulse : BOOL;
END_VAR
LD
  RUNG toggle
    /ld_Lamp ( ld_Lamp ) ( ld_Horn )
  RUNG first
    /ld_Done ( S ld_Done ) ( ld_Pulse )
END_LD
END_PROGRAM
