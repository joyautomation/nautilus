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
VAR_TEMP
    justDone : BOOL;
END_VAR
justDone := FALSE;
startEdge(CLK := Start);
IF Stop THEN
    state := 0;
END_IF;
CASE state OF
    0: (* idle *)
        IF startEdge.Q AND NOT Stop AND Recipe.TargetL > 0.0 THEN
            DosedL := 0.0;
            state := 10;
        END_IF;
    10: (* dosing: valve open, integrate the flow *)
        DosedL := DosedL + FlowLpm * Dt / 60.0;
        IF DosedL >= Recipe.TargetL THEN
            state := 20;
        ELSIF noFlow.Q THEN
            state := 90;
        END_IF;
    20: (* settling: valve shut, let the line drain *)
        IF settle.Q THEN
            Recipe.DoneCount := Recipe.DoneCount + 1;
            Recipe.LastDosedL := DosedL;
            justDone := TRUE;
            state := 0;
        END_IF;
END_CASE;
noFlow(IN := state = 10 AND FlowLpm < 0.5, PT := NoFlowTime);
settle(IN := state = 20, PT := Recipe.SettleTime);
doneHold(IN := justDone, PT := T#2S);
ValveOpen := state = 10;
Fault := state = 90;
Done := doneHold.Q;
Step := state;
END_FUNCTION_BLOCK
