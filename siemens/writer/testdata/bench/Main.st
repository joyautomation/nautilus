PROGRAM Main
VAR
    count : INT;
    pulse : R_TRIG;
    dwell : TON;
    batches : CTU;
    state : INT := 0;
    avg : REAL;
END_VAR
VAR RETAIN
    total : DINT;
END_VAR
VAR_TEMP
    i : INT;
END_VAR

(* the bench program: heartbeat, echo, a dwell timer and a batch counter *)
Heartbeat := Heartbeat + 1;
Echo := Command;

pulse(CLK := Start);
dwell(IN := Running AND NOT Fault, PT := T#2s);
batches(CU := pulse.Q, R := Reset, PV := 10, Q => Done);

IF dwell.Q THEN
    count := count + 1;
    total := total + INT_TO_DINT(count);
ELSIF Fault THEN
    count := 0;
ELSE
    count := MIN(count, 100);
END_IF;

CASE state OF
    0: IF Start THEN state := 1; END_IF;
    1, 2: state := state + 1;
    3..5: state := 0;
ELSE
    state := 0;
END_CASE;

avg := 0.0;
FOR i := 1 TO 4 DO
    avg := avg + INT_TO_REAL(i) / 4.0;
END_FOR;
Level := LIMIT(0.0, Level * 1.5 - (avg + 2.0), 100.0);
Running := NOT (Fault OR Reset) AND Start;
END_PROGRAM
