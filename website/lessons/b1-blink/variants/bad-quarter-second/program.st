PROGRAM Blink
VAR
    tick : TON;   (* counts half a second, then says so for one scan *)
END_VAR

tick(IN := NOT tick.Q, PT := T#250ms);

IF tick.Q THEN
    Lamp := NOT Lamp;
END_IF;
END_PROGRAM
