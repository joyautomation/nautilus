PROGRAM Sim
(* The plant itself, simulated in IEC — so this example runs, moves, and
   alarms with zero Go. A real flow balance now: the pump fills a 1000 L
   tank against a head-dependent demand draw, and an energy balance warms
   the contents against ambient loss and cool inlet mixing. A real
   deployment deletes this task and points the driver at the field (or
   keeps physics in Go — that tier still exists; see
   examples/heated-tank-fbd). *)
VAR_EXTERNAL
    LevelPct    : REAL;
    TempC       : REAL;
    SimDtS      : REAL;
    PumpRun     : BOOL;
    Heater      : REAL;
    Demand      : REAL;
    FT101       : REAL;
    FT102       : REAL;
    DV101       : REAL;
    SupplyTempC : REAL;
    HeaterKw    : REAL;
END_VAR
VAR CONSTANT
    CapacityL    : REAL := 1000.0; (* tank capacity, litres *)
    PumpMaxLps   : REAL := 2.5;    (* pump flow at 100% speed, litres/sec *)
    DemandMaxLps : REAL := 2.0;    (* outlet flow at 100% valve and full head *)
    HeaterMaxKw  : REAL := 240.0;  (* electric heater bank *)
    InletTempC   : REAL := 50.0;   (* warm supply water *)
    AmbientC     : REAL := 22.0;
    LossWPerK    : REAL := 800.0;  (* tank heat loss UA *)
    CpJPerKgK    : REAL := 4186.0;
    MinThermalKg : REAL := 50.0;   (* avoid divide-by-zero physics on an empty tank *)
END_VAR
VAR
    massKg : REAL;
    qNetW  : REAL;
END_VAR

(* DV-101: the demand valve tracks the operator's Demand input *)
DV101 := LIMIT(0.0, Demand, 100.0);

(* flows: pump inflow vs demand outflow — outflow depends on head (level) *)
IF PumpRun THEN
    FT101 := PumpMaxLps;
ELSE
    FT101 := 0.0;
END_IF;
FT102 := DV101 / 100.0 * DemandMaxLps * SQRT(LevelPct / 100.0);

(* tank: integrate the flow balance against capacity *)
LevelPct := LIMIT(0.0, LevelPct + (FT101 - FT102) * SimDtS / CapacityL * 100.0, 100.0);

(* energy balance: heater in, ambient loss out, cool inlet water mixing in.
   1 L water ~ 1 kg; the mass floor keeps an empty tank from dividing by
   zero. Clamped 0..103 °C — it's water in an open tank. *)
SupplyTempC := InletTempC;
HeaterKw := LIMIT(0.0, Heater, 100.0) / 100.0 * HeaterMaxKw;
massKg := MAX(LevelPct / 100.0 * CapacityL, MinThermalKg);
qNetW := HeaterKw * 1000.0 - LossWPerK * (TempC - AmbientC)
       + FT101 * CpJPerKgK * (SupplyTempC - TempC);
TempC := LIMIT(0.0, TempC + qNetW / (massKg * CpJPerKgK) * SimDtS, 103.0);
END_PROGRAM
