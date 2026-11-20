PROGRAM Blink
VAR n : INT; END_VAR
n := n + 1;
IF n >= 9 THEN
    Lamp := NOT Lamp;
    n := 0;
END_IF;
END_PROGRAM
