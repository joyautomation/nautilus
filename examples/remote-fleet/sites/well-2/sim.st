(* Bench-only plant for well-2: a smaller wet well than well-1's (a
   different span calibration) and a smaller pump (a lower max flow). *)
PROGRAM Sim
VAR_EXTERNAL
    WellLevel      : LREAL;
    InflowLps      : LREAL;
    Pump1          : Pump;
    Pump1_RunCmd   : BOOL;
    Pump1_SpeedCmd : LREAL;
    SimDtS         : LREAL;
END_VAR
VAR
    outflowLps : LREAL;
    tSec       : LREAL;
END_VAR
VAR CONSTANT
    InflowVaryFrac : LREAL := 0.20;
    InflowPeriodS  : LREAL := 150.0;
    TwoPi          : LREAL := 6.2831853;
END_VAR

tSec := tSec + SimDtS;

IF Pump1_RunCmd THEN
    Pump1.SpeedHz := LIMIT(0.0, Pump1.SpeedHz + LIMIT(-10.0 * SimDtS, Pump1_SpeedCmd - Pump1.SpeedHz, 10.0 * SimDtS), 55.0);
ELSE
    Pump1.SpeedHz := MAX(0.0, Pump1.SpeedHz - 10.0 * SimDtS);
END_IF;
Pump1.Run := Pump1.SpeedHz > 5.0;
Pump1.Fault := FALSE;

(* ~0 below 30 Hz, 8 L/s at 55 Hz — a smaller pump than well-1's. *)
IF Pump1.SpeedHz <= 30.0 THEN
    outflowLps := 0.0;
ELSE
    outflowLps := (Pump1.SpeedHz - 30.0) / (55.0 - 30.0) * 8.0;
END_IF;

(* 1100 L span over 100 % (11 L per %) — a smaller wet well than well-1's. *)
WellLevel := LIMIT(0.0, WellLevel + (InflowLps * (1.0 + InflowVaryFrac * SIN(tSec / InflowPeriodS * TwoPi)) - outflowLps) * SimDtS / 11.0, 100.0);
END_PROGRAM
