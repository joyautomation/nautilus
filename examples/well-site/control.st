PROGRAM Control
(* The site's control logic: the well pump fills the reservoir between
   two levels, the discharge valve opens with the pump, the hypochlorite
   pump doses while water moves, and the limit bits the alarms claim. *)
VAR_EXTERNAL
    WL101 : Well;
    FIT101 : Meter;
    FCV101 : Valve;
    CL101 : ChemFeed;
    R101 : Tank;
    StartLevel : REAL;
    StopLevel : REAL;
    MaxDepth : REAL;
    ResidualLow : REAL;
    ControlDtS : REAL;
END_VAR
VAR
    valveDisagreeS : REAL;   (* how long the valve has been off its command *)
END_VAR

(* the drawdown limit, with hysteresis so the alarm holds while the well
   recovers instead of chattering on the limit *)
IF WL101.Depth > MaxDepth THEN
    WL101.LL := TRUE;
ELSIF WL101.Depth < MaxDepth - 0.3 THEN
    WL101.LL := FALSE;
END_IF;
WL101.Fault := WL101.Pump.Fault;
CL101.Fault := CL101.Pump.Fault;

(* well pump seal-in on reservoir level; never on a fault, never when the
   water has been drawn down past the limit *)
IF WL101.Pump.Fault OR WL101.LL THEN
    WL101.Pump.Run := FALSE;
ELSIF R101.Level < StartLevel THEN
    WL101.Pump.Run := TRUE;
ELSIF R101.Level > StopLevel THEN
    WL101.Pump.Run := FALSE;
END_IF;
IF WL101.Pump.Running THEN
    WL101.Pump.Hours := WL101.Pump.Hours + ControlDtS / 3600.0;
END_IF;

(* the discharge valve opens with the pump's run command; a valve that is
   still off its command after a full stroke's worth of time is stuck *)
IF WL101.Pump.Run THEN
    FCV101.Cmd := 100.0;
ELSE
    FCV101.Cmd := 0.0;
END_IF;
IF ABS(FCV101.Cmd - FCV101.Pos) > 10.0 THEN
    valveDisagreeS := valveDisagreeS + ControlDtS;
ELSE
    valveDisagreeS := 0.0;
END_IF;
FCV101.Fault := valveDisagreeS > 8.0;

(* dose while the meter sees flow *)
CL101.Pump.Run := FIT101.Flow > 0.5 AND NOT CL101.Pump.Fault AND CL101.Tank.Level > 2.0;
CL101.Low := FIT101.Flow > 0.5 AND CL101.Residual < ResidualLow;
CL101.Tank.LL := CL101.Tank.Level < 10.0;

R101.HH := R101.Level > 95.0;
R101.LL := R101.Level < 10.0;
END_PROGRAM
