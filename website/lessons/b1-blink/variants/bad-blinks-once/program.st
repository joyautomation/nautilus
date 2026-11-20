PROGRAM Blink
VAR n : INT; flips : INT; END_VAR
n := n + 1;
IF n >= 5 AND flips < 2 THEN
    Lamp := NOT Lamp;
    flips := flips + 1;
    n := 0;
END_IF;
END_PROGRAM
