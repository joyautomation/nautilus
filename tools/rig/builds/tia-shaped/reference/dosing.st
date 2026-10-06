FUNCTION_BLOCK Dosing
(* One dosing line. Start's rising edge opens the valve until the recipe's
   TargetL has flowed, then waits SettleTime and counts the dose back into
   the recipe. No flow for NoFlowTime while dosing is a fault; Stop aborts
   from any state and clears a fault. *)
VAR_INPUT
    Start      : BOOL;
    Stop       : BOOL;
    FlowLpm    : REAL;  (* measured flow, L/min *)
    Dt         : REAL;  (* seconds since the last call *)
    NoFlowTime : TIME := T#5S;
END_VAR
VAR_OUTPUT
    ValveOpen : BOOL;
    Done      : BOOL;  (* held 2 s after a dose completes *)
    Fault     : BOOL;
    Step      : INT;
    DosedL    : REAL;
END_VAR
VAR_IN_OUT
    Recipe : DoseRecipe;
END_VAR
VAR
    state     : INT;
    startEdge : R_TRIG;
    noFlow    : TON;
    settle    : TON;
    doneHold  : TOF;
END_VAR
VAR CONSTANT
    S_IDLE   : INT := 0;
    S_DOSING : INT := 10;
    S_SETTLE : INT := 20;
    S_FAULT  : INT := 90;
END_VAR
VAR_TEMP
    justDone : BOOL;  (* Temp: FALSE at the start of every call *)
END_VAR
REGION sequence
    startEdge(CLK := Start);
    IF Stop THEN
        state := S_IDLE;
    END_IF;
    CASE state OF
        S_IDLE:
            IF startEdge.Q AND NOT Stop AND Recipe.TargetL > 0.0 THEN
                DosedL := 0.0;
                state := S_DOSING;
            END_IF;
        S_DOSING: (* valve open, integrate the flow *)
            DosedL := DosedL + FlowLpm * Dt / 60.0;
            IF DosedL >= Recipe.TargetL THEN
                state := S_SETTLE;
            ELSIF noFlow.Q THEN
                state := S_FAULT;
            END_IF;
        S_SETTLE: (* valve shut, let the line drain *)
            IF settle.Q THEN
                Recipe.DoneCount := Recipe.DoneCount + 1;
                Recipe.LastDosedL := DosedL;
                justDone := TRUE;
                state := S_IDLE;
            END_IF;
    END_CASE;
END_REGION
REGION timers
    noFlow(IN := state = S_DOSING AND FlowLpm < 0.5, PT := NoFlowTime);
    settle(IN := state = S_SETTLE, PT := Recipe.SettleTime);
    doneHold(IN := justDone, PT := T#2S);
END_REGION
ValveOpen := state = S_DOSING;
Fault := state = S_FAULT;
Done := doneHold.Q;
Step := state;
END_FUNCTION_BLOCK
