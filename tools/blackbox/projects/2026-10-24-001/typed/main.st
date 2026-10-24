PROGRAM Main
VAR_EXTERNAL
    Counter  : DINT;
    Ramp     : REAL;
    Blink    : BOOL;
    Fixed    : INT;
    P101     : Motor;
    Pumps    : ARRAY[1..2] OF Motor;
    ScanDt   : REAL;
END_VAR
VAR
    t : TON;
END_VAR
Counter := Counter + 1;
Ramp := Ramp + 0.5;
IF Ramp > 100.0 THEN Ramp := 0.0; END_IF;
Fixed := 7;                         (* same value every scan *)
t(IN := NOT t.Q, PT := T#1s);
IF t.Q THEN Blink := NOT Blink; END_IF;
P101.Drive.Speed := Ramp * 10.0;
P101.Starts := Counter;
Pumps[2].Drive.Amps := Ramp;
END_PROGRAM
