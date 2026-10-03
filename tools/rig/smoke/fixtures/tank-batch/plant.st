PROGRAM Plant
(* The tank, simulated in IEC so the batch runs with no hardware: the fill
   valve raises the level, the drain valve lowers it, the heater warms it
   against ambient loss. The auto-operator asks for the next batch whenever
   nothing is running. *)
VAR_EXTERNAL
    Level      : REAL;
    TempC      : REAL;
    Start      : BOOL;
    FillValve  : BOOL;
    DrainValve : BOOL;
    Heater     : BOOL;
    Mixer      : BOOL;
    PlantDtS   : REAL;
END_VAR

IF FillValve THEN
    Level := LIMIT(0.0, Level + 6.0 * PlantDtS, 100.0);
END_IF;
IF DrainValve THEN
    Level := LIMIT(0.0, Level - 5.0 * PlantDtS, 100.0);
END_IF;

IF Heater THEN
    TempC := TempC + 2.5 * PlantDtS;
ELSE
    TempC := TempC - 0.05 * (TempC - 20.0) * PlantDtS;
END_IF;
TempC := LIMIT(20.0, TempC, 120.0);

Start := NOT (FillValve OR DrainValve OR Heater OR Mixer);
END_PROGRAM
