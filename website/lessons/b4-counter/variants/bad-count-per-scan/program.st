PROGRAM Counter
VAR n : INT; END_VAR
IF Reset THEN n := 0; ELSIF Eye THEN n := n + 1; END_IF;
Count := n;
Full := n >= 5;
END_PROGRAM
