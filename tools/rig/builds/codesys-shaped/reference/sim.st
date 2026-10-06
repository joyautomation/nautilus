PROGRAM Sim
(* Bench-only plant: the drum fills through FillValve, drains through
   DrainPump, and the heater warms the water (it cools slowly toward
   20 degC otherwise). *)
VAR CONSTANT
    FillRate  : REAL := 5.0;   (* %/s *)
    DrainRate : REAL := 8.0;   (* %/s *)
    HeatRate  : REAL := 2.0;   (* degC/s *)
    CoolRate  : REAL := 0.05;  (* 1/s toward ambient *)
END_VAR
IF FillValve THEN
    LevelPct := LevelPct + FillRate * SimDtS;
END_IF;
IF DrainPump THEN
    LevelPct := LevelPct - DrainRate * SimDtS;
END_IF;
LevelPct := LIMIT(0.0, LevelPct, 100.0);
IF Heater AND LevelPct > 10.0 THEN
    TempC := TempC + HeatRate * SimDtS;
ELSE
    TempC := TempC - CoolRate * (TempC - 20.0) * SimDtS;
END_IF;
END_PROGRAM
