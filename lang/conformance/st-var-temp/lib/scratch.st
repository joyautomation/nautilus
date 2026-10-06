(* tmp is VAR_TEMP: IEC scratch for one call, so it starts every call at
   its declared 100. kept is VAR: it accumulates across calls (#203). *)
FUNCTION_BLOCK Scratch
VAR_INPUT
    Enable : BOOL;   (* the ladder rung's power; the block ignores it *)
    Add    : INT;
END_VAR
VAR_OUTPUT
    Fresh : INT;
    Total : INT;
END_VAR
VAR_TEMP
    tmp : INT := 100;
END_VAR
VAR
    kept : INT;
END_VAR
tmp := tmp + Add;
kept := kept + Add;
Fresh := tmp;
Total := kept;
END_FUNCTION_BLOCK
