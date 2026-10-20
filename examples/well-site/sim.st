PROGRAM Sim
(* Stands in for the field: the aquifer, the pipe, the reservoir and the
   chlorine, plus switches to make something go wrong on demand. *)
VAR_EXTERNAL
    WL101 : Well;
    FIT101 : Meter;
    FCV101 : Valve;
    CL101 : ChemFeed;
    R101 : Tank;
    SimDtS : REAL;
    InjectPumpFault : BOOL;
    InjectDrought : BOOL;
END_VAR
VAR CONSTANT
    WellMaxLps : REAL := 12.0;     (* at 100 % speed *)
    DrawdownMPerLps : REAL := 0.06; (* the cone of depression, per L/s *)
    AquiferTauS : REAL := 20.0;    (* how fast the level follows the pumping *)
    ReservoirM3 : REAL := 6.0;     (* tiny, so a fill is minutes, not hours *)
    TownLps : REAL := 5.0;         (* what the town draws, steadily *)
    ValveRatePctS : REAL := 20.0;
    PumpRampPctS : REAL := 25.0;
    DayTankPctPerS : REAL := 0.02;
END_VAR
VAR
    target : REAL;
    step : REAL;
END_VAR

(* the well pump *)
WL101.Pump.Fault := InjectPumpFault;
WL101.Pump.Running := WL101.Pump.Run AND NOT WL101.Pump.Fault;
IF WL101.Pump.Running THEN
    WL101.Pump.Speed := MIN(WL101.Pump.Speed + PumpRampPctS * SimDtS, 100.0);
ELSE
    WL101.Pump.Speed := MAX(WL101.Pump.Speed - PumpRampPctS * SimDtS, 0.0);
END_IF;
WL101.Flow := WL101.Pump.Speed / 100.0 * WellMaxLps;

(* the aquifer: the water level settles toward static plus the drawdown
   the flow earns, with a lag; a drought sinks the static level *)
IF InjectDrought THEN
    WL101.Static := 1.15;
ELSE
    WL101.Static := 0.45;
END_IF;
target := WL101.Static + DrawdownMPerLps * WL101.Flow;
WL101.Depth := WL101.Depth + (target - WL101.Depth) * SimDtS / AquiferTauS;

(* the discharge valve strokes toward its command *)
step := ValveRatePctS * SimDtS;
FCV101.Pos := FCV101.Pos + LIMIT(-step, FCV101.Cmd - FCV101.Pos, step);

(* the meter sees what the valve passes *)
FIT101.Flow := WL101.Flow * FCV101.Pos / 100.0;
FIT101.Total := FIT101.Total + FIT101.Flow * SimDtS / 1000.0;

(* the reservoir: fills from the well, drains to the town *)
R101.Level := LIMIT(0.0, R101.Level + (FIT101.Flow - TownLps) * SimDtS / ReservoirM3 / 10.0, 100.0);
R101.TempC := 14.0;

(* hypochlorite: the dosing pump draws the day tank down and holds the
   residual while it runs; the residual decays when it stops *)
CL101.Pump.Running := CL101.Pump.Run AND NOT CL101.Pump.Fault;
IF CL101.Pump.Running THEN
    CL101.Pump.Speed := 100.0;
    CL101.Tank.Level := MAX(CL101.Tank.Level - DayTankPctPerS * SimDtS, 0.0);
    CL101.Residual := CL101.Residual + (1.2 - CL101.Residual) * SimDtS / 15.0;
ELSE
    CL101.Pump.Speed := 0.0;
    CL101.Residual := MAX(CL101.Residual - 0.05 * SimDtS, 0.0);
END_IF;
END_PROGRAM
