(* well-2's own control: the same shape as ../well-1/well.st, deliberately
   duplicated rather than shared — each site in this fleet deploys alone.
   Different setpoints live in this project's nautilus.yaml. *)
PROGRAM Well
VAR_EXTERNAL
    WellLevel     : LREAL;
    Pump1         : Pump;
    Pump1_RunCmd  : BOOL;
    Pump1_SpeedCmd : LREAL;
    PumpOnLevel   : LREAL;
    PumpOffLevel  : LREAL;
    MinSpeedHz    : LREAL;
    MaxSpeedHz    : LREAL;
    PumpStarts    : INT;
    PumpHours     : LREAL;
    WellDtS       : LREAL;
END_VAR
VAR
    edgeUp : R_TRIG;
END_VAR

Pump1_RunCmd := (WellLevel >= PumpOnLevel) OR (Pump1_RunCmd AND WellLevel > PumpOffLevel);

IF Pump1_RunCmd THEN
    Pump1_SpeedCmd := LIMIT(MinSpeedHz, Pump1.SpeedSP, MaxSpeedHz);
ELSE
    Pump1_SpeedCmd := 0.0;
END_IF;

edgeUp(CLK := Pump1.Run);
IF edgeUp.Q THEN
    PumpStarts := PumpStarts + 1;
END_IF;
IF Pump1.Run THEN
    PumpHours := PumpHours + WellDtS / 3600.0;
END_IF;
END_PROGRAM
