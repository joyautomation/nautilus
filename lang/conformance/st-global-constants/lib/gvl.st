(* A Codesys-style GVL of constants (#176): project-wide, folded at compile
   time like a local VAR CONSTANT, visible to every POU in every language.
   A constant is not a tag — it has no entry in nautilus.yaml. *)
VAR_GLOBAL CONSTANT
    HI_LIMIT : REAL := 80.0;
    T_DELAY  : TIME := T#30ms;
    STAGES   : INT := 2 + 1;
END_VAR
