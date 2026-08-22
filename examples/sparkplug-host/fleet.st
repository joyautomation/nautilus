(* fleet.st — rollups over the whole consumed fleet.
   Every site's data tag reads "fault until first birth": __Online guards
   every one of them below, which is the rule the guide documents loudly.
   A never-birthed site would otherwise fault this program's scan on its
   very first reference to that site's data. *)
PROGRAM Main
VAR_EXTERNAL
    W6__Online          : BOOL;
    BP2__Online         : BOOL;
    CL1__Online         : BOOL;
    SitesOnline         : INT;

    W6_Pump1            : Motor;
    BP2_plc1_Pump_Run   : BOOL;
    AnyPumpRunning      : BOOL;

    W6_Well_Level       : LREAL;
    WellLowLevelSP      : LREAL;
    WellLowLevelAlm     : BOOL;

    (* A MEMBER output: W6_Pump1_Speed writes Pump1.Speed inside W6's UDT.
       Manifest `member: Speed` on the Pump1 binding makes it a plain LREAL
       here; the driver sends an NCMD carrying a partial template, so the
       members W6 is driving itself (Run) are never touched. *)
    W6_Pump1_Speed      : LREAL;
    PumpSpeedCmd        : LREAL;
END_VAR
VAR
    LevelLow : TON; (* 10 s proven — a level glitch on the wire, or one
                        scan of noise, must not sound the alarm *)
END_VAR

SitesOnline := 0;
IF W6__Online THEN
    SitesOnline := SitesOnline + 1;
END_IF;
IF BP2__Online THEN
    SitesOnline := SitesOnline + 1;
END_IF;
IF CL1__Online THEN
    SitesOnline := SitesOnline + 1;
END_IF;

AnyPumpRunning := (W6__Online AND W6_Pump1.Run) OR (BP2__Online AND BP2_plc1_Pump_Run);

LevelLow(IN := W6__Online AND (W6_Well_Level < WellLowLevelSP), PT := T#10S);
WellLowLevelAlm := LevelLow.Q;

(* Forward the operator's speed command into W6's pump UDT, interlocked on
   __Online. Holding 0 while the site is dark means a site coming back does
   not inherit a stale command the operator has long since forgotten. *)
IF W6__Online THEN
    W6_Pump1_Speed := PumpSpeedCmd;
ELSE
    W6_Pump1_Speed := 0.0;
END_IF;
END_PROGRAM
