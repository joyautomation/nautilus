PROGRAM Pumps
(* Duplex pump station. Lead pump starts at LeadStartPct, lag at
   LagStartPct, both stop at StopPct. The lead alternates each time the
   station finishes a pump-down. High level alarms after HiLevelDelay. *)
VAR
    leadCall   : BOOL;
    lagCall    : BOOL;
    anyRunning : BOOL;
    hiTon      : TON;
    cycleEnd   : F_TRIG;
    cycleCount : CTU;
    p1Meter    : RunHours;
    p2Meter    : RunHours;
END_VAR

(* Level calls, each with hysteresis down to the stop level *)
IF LevelPct >= LeadStartPct THEN
    leadCall := TRUE;
ELSIF LevelPct <= StopPct THEN
    leadCall := FALSE;
END_IF;
IF LevelPct >= LagStartPct THEN
    lagCall := TRUE;
ELSIF LevelPct <= StopPct THEN
    lagCall := FALSE;
END_IF;

(* Calls to pumps, by which one leads *)
IF Lead = Pump1 THEN
    P1_Run := leadCall;
    P2_Run := lagCall;
ELSE
    P2_Run := leadCall;
    P1_Run := lagCall;
END_IF;

anyRunning := P1_Run OR P2_Run;
IF P1_Run AND P2_Run THEN
    Station := BothRunning;
ELSIF anyRunning THEN
    Station := LeadRunning;
ELSE
    Station := Idle;
END_IF;

(* Alternate the lead on every completed pump-down *)
cycleEnd(CLK := anyRunning);
IF cycleEnd.Q THEN
    IF Lead = Pump1 THEN
        Lead := Pump2;
    ELSE
        Lead := Pump1;
    END_IF;
END_IF;
cycleCount(CU := cycleEnd.Q, R := FALSE, PV := 32767);
Cycles := cycleCount.CV;

(* High level, after the delay *)
hiTon(IN := LevelPct >= HiLevelPct, PT := HiLevelDelay);
HiLevelAlm := hiTon.Q;

(* Run hours per pump *)
p1Meter(Running := P1_Run, Dt := ScanDtS, Stats := P1_Stats);
p2Meter(Running := P2_Run, Dt := ScanDtS, Stats := P2_Stats);
END_PROGRAM
