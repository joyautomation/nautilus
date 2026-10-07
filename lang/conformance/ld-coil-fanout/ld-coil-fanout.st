PROGRAM FanoutSt
(* The reference: each rung's condition is evaluated once, before any of
   its coils is written. *)
VAR_EXTERNAL
    st_Lamp, st_Horn, st_Done, st_Pulse : BOOL;
END_VAR
VAR
    toggle, first : BOOL;
END_VAR

(* /Lamp ( Lamp ) ( Horn ): a toggle; Horn follows the rung, not the
   Lamp the first coil just wrote. *)
toggle := NOT st_Lamp;
st_Lamp := toggle;
st_Horn := toggle;

(* /Done ( S Done ) ( Pulse ): Pulse is TRUE on the scan the rung sets
   Done, and never again. *)
first := NOT st_Done;
IF first THEN st_Done := TRUE; END_IF;
st_Pulse := first;
END_PROGRAM
