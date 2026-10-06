PROGRAM Main
VAR_EXTERNAL
    Status : DINT;
    Cmd    : DINT;
    Go     : BOOL;
    Ready  : BOOL;
    Low3   : BOOL;
    Word2  : DINT;
END_VAR
LD
  RUNG ready (* bit 0 set and bit 15 clear *)
    Status.0 /Status.15 ( Ready )

  RUNG cmd (* coils on bits of a command word *)
    Go ( Cmd.4 ) ( S Cmd.5 )

  RUNG copy (* a bit copied into another word's bit: set and reset coils (a MOVE takes no BOOL) *)
    Go Status.1 ( S Cmd.7 )

  RUNG copy2
    Go /Status.1 ( R Cmd.7 )

  RUNG copy3
    Go Status.0 ( S Word2.3 )

  RUNG copy4
    Go /Status.0 ( R Word2.3 )

  RUNG mask (* the low three bits as a number *)
    EQ(Status AND 7, 3) ( Low3 )

  RUNG clear
    /Go ( R Cmd.5 ) ( R Cmd.7 )
END_LD
END_PROGRAM
