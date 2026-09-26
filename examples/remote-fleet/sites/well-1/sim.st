(* Bench-only plant: the wet well's level (an inflow/outflow balance) and
   the pump/VFD's speed response. A field deployment deletes this task
   and points well.st's Pump1/WellLevel tags at real hardware instead
   (a Modbus VFD and level transmitter — see ../booster-1/devices.yaml
   for the pattern this fleet uses elsewhere). *)
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
    InflowPeriodS  : LREAL := 180.0;
    TwoPi          : LREAL := 6.2831853;
END_VAR

tSec := tSec + SimDtS;

(* Speed ramps toward the command at 10 Hz/s; running is "spinning". *)
IF Pump1_RunCmd THEN
    Pump1.SpeedHz := LIMIT(0.0, Pump1.SpeedHz + LIMIT(-10.0 * SimDtS, Pump1_SpeedCmd - Pump1.SpeedHz, 10.0 * SimDtS), 60.0);
ELSE
    Pump1.SpeedHz := MAX(0.0, Pump1.SpeedHz - 10.0 * SimDtS);
END_IF;
Pump1.Run := Pump1.SpeedHz > 5.0;
Pump1.Fault := FALSE;

(* Discharge flow: ~0 below 25 Hz, 10 L/s at 60 Hz, linear between — a
   small well pump, a tenth the size of lift-station's lift pumps. *)
IF Pump1.SpeedHz <= 25.0 THEN
    outflowLps := 0.0;
ELSE
    outflowLps := (Pump1.SpeedHz - 25.0) / (60.0 - 25.0) * 10.0;
END_IF;

(* 1500 L span over 100 % (15 L per %) plus a slow sinusoid on the base
   inflow, so the level never sits dead flat. *)
WellLevel := LIMIT(0.0, WellLevel + (InflowLps * (1.0 + InflowVaryFrac * SIN(tSec / InflowPeriodS * TwoPi)) - outflowLps) * SimDtS / 15.0, 100.0);
END_PROGRAM
