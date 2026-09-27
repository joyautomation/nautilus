PROGRAM Control
(* The rig's control logic: P-101 fills T-101 between two levels, XV-101
   follows the operator's demand, and the limit bits the alarms claim. *)
VAR_EXTERNAL
    T101 : Tank;
    P101 : Motor;
    XV101 : Valve;
    SK101 : Skid;
    SK102 : Skid;
    Demand : REAL;
    StartLevel : REAL;
    StopLevel : REAL;
    LevelHH : REAL;
    LevelLL : REAL;
    ControlDtS : REAL;
END_VAR

(* pump seal-in: start below StartLevel, stop above StopLevel, never on a fault *)
IF P101.Fault THEN
    P101.Run := FALSE;
ELSIF T101.Level < StartLevel THEN
    P101.Run := TRUE;
ELSIF T101.Level > StopLevel THEN
    P101.Run := FALSE;
END_IF;

IF P101.Running THEN
    P101.Hours := P101.Hours + ControlDtS / 3600.0;
END_IF;

XV101.Cmd := LIMIT(0.0, Demand, 100.0);
XV101.Fault := ABS(XV101.Cmd - XV101.Pos) > 10.0;

T101.HH := T101.Level > LevelHH;
T101.LL := T101.Level < LevelLL;

(* transfer skids: the duty skid runs while the drain passes flow, its valve
   opens with its pump; the standby takes over when the duty pump faults *)
SK101.Pump.Run := XV101.Pos > 5.0 AND NOT SK101.Pump.Fault;
SK102.Pump.Run := XV101.Pos > 5.0 AND SK101.Pump.Fault AND NOT SK102.Pump.Fault;
IF SK101.Pump.Run THEN
    SK101.Valve.Cmd := 100.0;
ELSE
    SK101.Valve.Cmd := 0.0;
END_IF;
IF SK102.Pump.Run THEN
    SK102.Valve.Cmd := 100.0;
ELSE
    SK102.Valve.Cmd := 0.0;
END_IF;
SK101.Fault := SK101.Pump.Fault;
SK102.Fault := SK102.Pump.Fault;
END_PROGRAM
