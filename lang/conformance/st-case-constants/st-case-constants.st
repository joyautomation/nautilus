PROGRAM CaseConstSt
(* The states are named constants, the way a TIA or Codesys state machine
   is written. Each label ends the clause before it. *)
VAR_EXTERNAL
    Go       : BOOL;
    st_State : INT;
    st_Count : INT;
END_VAR
VAR CONSTANT
    S_IDLE : INT := 0;
    S_RUN  : INT := 10;
    S_DONE : INT := 20;
    RUNS   : INT := 3;
END_VAR
CASE st_State OF
    S_IDLE:
        IF Go THEN st_State := S_RUN; END_IF;
    S_RUN:
        st_Count := st_Count + 1;
        IF st_Count >= RUNS THEN st_State := S_DONE; END_IF;
    S_DONE:
        IF NOT Go THEN st_State := S_IDLE; END_IF;
END_CASE;
END_PROGRAM
