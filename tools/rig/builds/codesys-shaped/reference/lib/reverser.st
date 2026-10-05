(* FB_Reverser — the wash drum's reversing rhythm: run clockwise for
   tRun, pause tPause, run counter-clockwise for tRun, pause, repeat, for
   as long as Enable is TRUE. Enable FALSE stops the drum and restarts
   the rhythm from clockwise. A library block (lib/ holds libraries
   only), called from washer.sfc's DrumCtl action. *)
FUNCTION_BLOCK FB_Reverser
VAR_INPUT
    Enable : BOOL;
    tRun   : TIME := T#4S;
    tPause : TIME := T#1S;
END_VAR
VAR_OUTPUT
    CW  : BOOL;
    CCW : BOOL;
END_VAR
VAR
    phase : INT;   (* 0 CW, 1 pause, 2 CCW, 3 pause *)
    tmr   : TON;
END_VAR
IF NOT Enable THEN
    phase := 0;
    tmr(IN := FALSE);
ELSE
    IF phase = 0 OR phase = 2 THEN
        tmr(IN := TRUE, PT := tRun);
    ELSE
        tmr(IN := TRUE, PT := tPause);
    END_IF;
    IF tmr.Q THEN
        phase := (phase + 1) MOD 4;
        tmr(IN := FALSE);
    END_IF;
END_IF;
CW := Enable AND phase = 0;
CCW := Enable AND phase = 2;
END_FUNCTION_BLOCK
