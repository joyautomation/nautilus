PROGRAM Control
(* The rig's control logic: P-101 fills T-101 between two levels, XV-101
   follows the operator's demand, and the limit bits the alarms claim. *)
VAR_EXTERNAL
    T101 : Tank;
    P101 : Motor;
    XV101 : Valve;
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
END_PROGRAM
