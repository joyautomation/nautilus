PROGRAM GConstSt
(* HI_LIMIT, T_DELAY and STAGES come from lib/gvl.st; nothing declares them
   here. STAGES sizes an array and bounds a loop. *)
VAR_EXTERNAL
    Level     : REAL;
    Run       : BOOL;
    st_High   : BOOL;
    st_Done   : BOOL;
    st_Stages : INT;
END_VAR
VAR
    t     : TON;
    stage : ARRAY[1..STAGES] OF INT;
    i     : INT;
END_VAR
st_High := Level > HI_LIMIT;
t(IN := Run, PT := T_DELAY);
st_Done := t.Q;
st_Stages := 0;
FOR i := 1 TO STAGES DO
    stage[i] := i;
    st_Stages := st_Stages + stage[i];
END_FOR;
END_PROGRAM
