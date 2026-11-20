PROGRAM Counter
VAR was : BOOL; n : INT; END_VAR
IF Reset THEN
    n := 0;
ELSIF Eye AND NOT was THEN
    n := n + 1;
END_IF;
was := Eye;
Count := n;
Full := n >= 5;
END_PROGRAM
