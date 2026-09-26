(* Bench-only plant: the two pumps' speed response, the discharge/suction
   pressure this station's PID sees, and the shared distribution tank
   level (fed by the two wells elsewhere in the fleet, drained by system
   demand here — this bench model doesn't cross projects, so it just
   drifts on its own slow cycle). A field deployment deletes this task;
   the flow meter (FM1_FlowLps) is real either way — see devices.yaml. *)
PROGRAM Sim
VAR_EXTERNAL
    DischargePress : LREAL;
    SuctionPress   : LREAL;
    TankLevel      : LREAL;

    Pump1          : Pump;
    Pump2          : Pump;
    Pump1_RunCmd   : BOOL;
    Pump2_RunCmd   : BOOL;
    Pump1_SpeedCmd : LREAL;
    Pump2_SpeedCmd : LREAL;

    SimDtS         : LREAL;
END_VAR
VAR
    tSec       : LREAL;
    totalSpeed : LREAL;
END_VAR
VAR CONSTANT
    TankPeriodS : LREAL := 300.0;
    TwoPi       : LREAL := 6.2831853;
END_VAR

tSec := tSec + SimDtS;

(* Speed ramps toward each pump's command at 10 Hz/s while running. *)
IF Pump1_RunCmd THEN
    Pump1.SpeedHz := LIMIT(0.0, Pump1.SpeedHz + LIMIT(-10.0 * SimDtS, Pump1_SpeedCmd - Pump1.SpeedHz, 10.0 * SimDtS), 60.0);
ELSE
    Pump1.SpeedHz := MAX(0.0, Pump1.SpeedHz - 10.0 * SimDtS);
END_IF;
Pump1.Run := Pump1.SpeedHz > 5.0;
Pump1.Fault := FALSE;

IF Pump2_RunCmd THEN
    Pump2.SpeedHz := LIMIT(0.0, Pump2.SpeedHz + LIMIT(-10.0 * SimDtS, Pump2_SpeedCmd - Pump2.SpeedHz, 10.0 * SimDtS), 60.0);
ELSE
    Pump2.SpeedHz := MAX(0.0, Pump2.SpeedHz - 10.0 * SimDtS);
END_IF;
Pump2.Run := Pump2.SpeedHz > 5.0;
Pump2.Fault := FALSE;

(* Discharge pressure rises with combined pump speed, roughly — enough of
   a plant model for the PID to have something real to close a loop
   against. 40 psi static (suction-side) plus ~0.4 psi per combined Hz. *)
totalSpeed := Pump1.SpeedHz + Pump2.SpeedHz;
DischargePress := 40.0 + totalSpeed * 0.4;
SuctionPress := 40.0 - (TankLevel - 50.0) * 0.05;

(* The shared tank: a slow sinusoid standing in for the wells' fill and
   the system's draw — this bench model is deliberately not wired to the
   well sites' own sims (each site runs standalone; see ../../README.md). *)
TankLevel := LIMIT(0.0, 55.0 + 20.0 * SIN(tSec / TankPeriodS * TwoPi), 100.0);
END_PROGRAM
