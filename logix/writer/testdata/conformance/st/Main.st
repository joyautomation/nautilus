PROGRAM STConformance
(* Structured text on both runtimes: arithmetic, a hysteresis alarm,
   CASE, a FOR loop, a timer and a counter called the IEC way. *)
VAR_EXTERNAL
    In      : BOOL;
    Level   : REAL;
    Mode    : INT;
    Pulse   : BOOL;
    Reset   : BOOL;
    Out     : REAL;
    HiAlm   : BOOL;
    Delayed : BOOL;
    Counted : BOOL;
    Sum     : DINT;
END_VAR
VAR
    t : TON;
    c : CTU;
    i : INT;
END_VAR

CASE Mode OF
    0: Out := Level;
    1: Out := Level * 2.0;
    2..3: Out := ABS(Level - 100.0);
ELSE
    Out := 0.0;
END_CASE;

IF Level > 80.0 THEN
    HiAlm := TRUE;
ELSIF Level < 70.0 THEN
    HiAlm := FALSE;
END_IF;

Sum := 0;
FOR i := 1 TO 4 DO
    Sum := Sum + i;
END_FOR;

t(IN := In, PT := T#2S, Q => Delayed);
c(CU := Pulse, PV := 3, R := Reset, Q => Counted);
END_PROGRAM
