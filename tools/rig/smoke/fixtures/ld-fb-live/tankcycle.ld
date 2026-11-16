(* The block the rungs live in. Sealed is internal (a VAR, no pin): before
   the fix the ladder showed no value for it at all. *)
FUNCTION_BLOCK TankCycle
VAR_INPUT
    Start : BOOL;
    Stop  : BOOL;
END_VAR
VAR_OUTPUT
    Running : BOOL;
END_VAR
VAR
    Sealed : BOOL;
END_VAR
LD
  RUNG seal (* start/stop seal-in on an internal bit *)
    [ Start | Sealed ] Stop ( Sealed )

  RUNG run (* the block runs while sealed in *)
    Sealed ( Running )
END_LD
END_FUNCTION_BLOCK
