PROGRAM Sim
(* Stands in for the field. The props on the office rig are dumb objects;
   this task gives them a moving process, plus fault-injection switches so
   an AR panel has alarms and bad states to show on demand. *)
VAR_EXTERNAL
    T101 : Tank;
    P101 : Motor;
    XV101 : Valve;
    SK101 : Skid;
    SK102 : Skid;
    SimDtS : REAL;
    InjectPumpFault : BOOL;
    InjectValveStick : BOOL;
END_VAR
VAR CONSTANT
    CapacityL  : REAL := 100.0;   (* small, so a fill/drain cycle is ~2 min *)
    PumpMaxLps : REAL := 1.0;
    DrainMaxLps : REAL := 0.8;
    ValveRatePctS : REAL := 20.0;  (* stroke speed *)
    PumpRampPctS : REAL := 25.0;
END_VAR
VAR
    inLps : REAL;
    outLps : REAL;
    step : REAL;
END_VAR

P101.Fault := InjectPumpFault;
P101.Running := P101.Run AND NOT P101.Fault;
IF P101.Running THEN
    P101.Speed := MIN(P101.Speed + PumpRampPctS * SimDtS, 100.0);
ELSE
    P101.Speed := MAX(P101.Speed - PumpRampPctS * SimDtS, 0.0);
END_IF;

(* the valve strokes toward its command unless it is stuck *)
IF NOT InjectValveStick THEN
    step := ValveRatePctS * SimDtS;
    XV101.Pos := XV101.Pos + LIMIT(-step, XV101.Cmd - XV101.Pos, step);
END_IF;

inLps := P101.Speed / 100.0 * PumpMaxLps;
outLps := XV101.Pos / 100.0 * DrainMaxLps * SQRT(T101.Level / 100.0);
T101.Level := LIMIT(0.0, T101.Level + (inLps - outLps) * SimDtS / CapacityL * 100.0, 100.0);
T101.TempC := 20.0 + 0.05 * P101.Speed;

(* the transfer skids: the injected pump fault trips the duty skid too, so
   the standby has something to take over from *)
SK101.Pump.Fault := InjectPumpFault;
SK101.Pump.Running := SK101.Pump.Run AND NOT SK101.Pump.Fault;
IF SK101.Pump.Running THEN
    SK101.Pump.Speed := MIN(SK101.Pump.Speed + PumpRampPctS * SimDtS, 100.0);
ELSE
    SK101.Pump.Speed := MAX(SK101.Pump.Speed - PumpRampPctS * SimDtS, 0.0);
END_IF;
step := ValveRatePctS * SimDtS;
SK101.Valve.Pos := SK101.Valve.Pos + LIMIT(-step, SK101.Valve.Cmd - SK101.Valve.Pos, step);

SK102.Pump.Running := SK102.Pump.Run AND NOT SK102.Pump.Fault;
IF SK102.Pump.Running THEN
    SK102.Pump.Speed := MIN(SK102.Pump.Speed + PumpRampPctS * SimDtS, 100.0);
ELSE
    SK102.Pump.Speed := MAX(SK102.Pump.Speed - PumpRampPctS * SimDtS, 0.0);
END_IF;
SK102.Valve.Pos := SK102.Valve.Pos + LIMIT(-step, SK102.Valve.Cmd - SK102.Valve.Pos, step);
END_PROGRAM
