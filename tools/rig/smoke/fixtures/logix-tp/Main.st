PROGRAM Main
VAR_EXTERNAL
    Go  : BOOL;
    Out : BOOL;
END_VAR
VAR
    pulse : TP;
END_VAR
pulse(IN := Go, PT := T#500MS);
Out := pulse.Q;
END_PROGRAM
