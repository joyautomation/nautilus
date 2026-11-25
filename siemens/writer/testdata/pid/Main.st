PROGRAM Main
VAR_EXTERNAL
    Auto : BOOL;
    Level : REAL;
    LevelSP : REAL;
    LevelDtS : REAL;
    Valve : REAL;
    High : BOOL;
    Every3 : BOOL;
    Pump : REAL;
    Heat : REAL;
    Held : REAL;
END_VAR
VAR
    lic : PID;
    pic : PID;
    heater : Loop;
END_VAR
VAR RETAIN
    starts : DINT;
    hold : PID;
END_VAR
lic(AUTO := Auto, PV := Level, SP := LevelSP, KP := 1.5, KI := 0.2, KD := 0.5,
    CV_MAN := 10.0, CV_MIN := 0.0, CV_MAX := 100.0, DIRECT := TRUE, DT := LevelDtS, DB := 0.25,
    RESET := FALSE, CV => Valve);
High := lic.SAT_HI;
IF Every3 THEN
    pic(AUTO := TRUE, PV := Level, SP := 40.0, KP := 1.0, KI := 0.1);
END_IF;
Pump := pic.CV;
heater(Enable := Auto, Meas := Level, Target := LevelSP);
Heat := heater.Out;
hold(AUTO := Auto, PV := Level, SP := LevelSP, KP := 1.0, KI := 1.0);
Held := hold.CV;
starts := starts + 1;
END_PROGRAM
