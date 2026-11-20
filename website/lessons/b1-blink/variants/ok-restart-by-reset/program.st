PROGRAM Blink
VAR
    tick : TON;
    flip : BOOL;
END_VAR
tick(IN := NOT flip, PT := T#500ms);
flip := tick.Q;
IF flip THEN
    Lamp := NOT Lamp;
END_IF;
END_PROGRAM
