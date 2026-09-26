(* A LADDER LIBRARY: FUNCTION_BLOCKs written as rungs, no PROGRAM.

   A .ld file with no PROGRAM is a library, exactly like a .st one — its
   blocks join the project prelude, so every program in the project can
   instantiate them, in any language. This is what ladder has instead of
   a JSR: a subroutine with a real interface, called as many times as you
   like, each instance carrying its own retained state.

   Open it in VS Code with "Open With -> Ladder Diagram": each block's
   rungs render under its own FUNCTION_BLOCK heading. *)

FUNCTION_BLOCK PumpSeq
VAR_INPUT
    Start     : BOOL; (* rung power lands here — the first free BOOL input *)
    Stop      : BOOL;
    Level     : REAL;
    StopLevel : REAL;
END_VAR
VAR_OUTPUT
    Run  : BOOL;      (* power leaves here — the first BOOL output *)
    Warm : BOOL;
END_VAR
VAR
    t1 : TON;         (* retained per INSTANCE, not per block *)
END_VAR
LD
  RUNG seal (* start seals in; stop, or the tank filling, drops it *)
    [ Start | Run ] /Stop /GE(Level, StopLevel) ( Run )

  RUNG warm (* five seconds of continuous run and the pump is warm *)
    Run t1:TON(PT := T#5S) ( Warm )
END_LD
END_FUNCTION_BLOCK

FUNCTION_BLOCK PumpFail
VAR_INPUT
    Run     : BOOL;
    Flowing : BOOL;
END_VAR
VAR_OUTPUT
    Alarm : BOOL;
END_VAR
VAR
    t2 : TON;
END_VAR
LD
  RUNG nofl (* commanded to run, no flow for 3 s: failed to start *)
    Run /Flowing t2:TON(PT := T#3S) ( Alarm )
END_LD
END_FUNCTION_BLOCK
