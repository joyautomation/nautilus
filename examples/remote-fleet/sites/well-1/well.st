(* well-1's own control: a single pump, called on rising wet-well level
   with hysteresis, run at a speed clamped to [MinSpeedHz, MaxSpeedHz].
   Pump1 is a Sparkplug Template (TYPE Pump, types.st) — the SAME shape
   scada/sites.yaml declares for all three sites, but this file, not a
   shared library, is where it lives: each site deploys alone (see
   ../../README.md). *)
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

(* Seal-in hysteresis: call the pump at PumpOnLevel, hold it until the
   level drops back through PumpOffLevel — same idiom as lift-station's
   HighLevelAlm seal-in, one line instead of a state machine. *)
Pump1_RunCmd := (WellLevel >= PumpOnLevel) OR (Pump1_RunCmd AND WellLevel > PumpOffLevel);

IF Pump1_RunCmd THEN
    Pump1_SpeedCmd := LIMIT(MinSpeedHz, Pump1.SpeedSP, MaxSpeedHz);
ELSE
    Pump1_SpeedCmd := 0.0;
END_IF;

(* Run-hours meter: starts on Run's rising edge, hours while running. *)
edgeUp(CLK := Pump1.Run);
IF edgeUp.Q THEN
    PumpStarts := PumpStarts + 1;
END_IF;
IF Pump1.Run THEN
    PumpHours := PumpHours + WellDtS / 3600.0;
END_IF;
END_PROGRAM
