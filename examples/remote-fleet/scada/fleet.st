(* fleet.st — rollups over the whole consumed fleet, plus the plant's own
   local chlorine analyser. Every site's data tag reads "fault until first
   birth": __Online guards every one of them below, which is the rule
   examples/sparkplug-host's fleet.st (and the guide) call out loudly. A
   never-birthed site would otherwise fault this program's scan on its
   very first reference to that site's data. *)
PROGRAM Main
VAR_EXTERNAL
    Well1__Online     : BOOL;
    Well2__Online     : BOOL;
    Booster1__Online  : BOOL;
    Booster1_plc1__Online : BOOL;
    SitesOnline       : INT;

    Well1_Pump1       : Pump;
    Well2_Pump1       : Pump;
    Booster1_Pump1    : Pump;
    Booster1_Pump2    : Pump;
    AnyPumpRunning    : BOOL;

    Booster1_plc1_FM1_FlowLps : LREAL;
    SystemDemandLps   : LREAL;

    Booster1_TankLevel : LREAL;
    TankLevelPct       : LREAL;
    TankLowSP          : LREAL;
    TankLowAlm         : BOOL;

    CL1_ResidualPpm    : LREAL;
    ResidualLowSP      : LREAL;
    ChlorineResidualLowAlm : BOOL;
END_VAR

SitesOnline := 0;
IF Well1__Online THEN
    SitesOnline := SitesOnline + 1;
END_IF;
IF Well2__Online THEN
    SitesOnline := SitesOnline + 1;
END_IF;
IF Booster1__Online THEN
    SitesOnline := SitesOnline + 1;
END_IF;

AnyPumpRunning := (Well1__Online AND Well1_Pump1.Run)
    OR (Well2__Online AND Well2_Pump1.Run)
    OR (Booster1__Online AND (Booster1_Pump1.Run OR Booster1_Pump2.Run));

(* The system's own flow demand: Booster 1's discharge flow meter,
   guarded on ITS device's own online companion — a Modbus device
   attached to a Sparkplug node has its own __Online distinct from the
   node's. *)
IF Booster1_plc1__Online THEN
    SystemDemandLps := Booster1_plc1_FM1_FlowLps;
ELSE
    SystemDemandLps := 0.0;
END_IF;

(* The tank rollup — one site's reading, guarded the same way every other
   fleet value is. A dark Booster 1 reads as an empty tank, not a frozen
   last-known level: conservative, and consistent with "never trust a
   dead site's last value" everywhere else in this project. *)
IF Booster1__Online THEN
    TankLevelPct := Booster1_TankLevel;
ELSE
    TankLevelPct := 0.0;
END_IF;
TankLowAlm := TankLevelPct < TankLowSP;

(* The plant's own local device — no Sparkplug __Online to guard on; a
   disconnected Modbus source just holds its last-read value, same
   contract as every other field driver in this codebase. *)
ChlorineResidualLowAlm := CL1_ResidualPpm < ResidualLowSP;
END_PROGRAM
