PROGRAM Stats
(* Runtime hours/starts for each pump (RuntimeMeter, lib/pump.st) and the
   station flow totalizer. Shared, unchanged, between the bench and field
   manifests — it only reads process values and writes rollups, never a
   role that differs between them. *)
VAR_EXTERNAL
    P101_Running : BOOL;
    P102_Running : BOOL;
    P101_Stats   : PumpStats;
    P102_Stats   : PumpStats;
    FIT101_Flow  : REAL;
    StationFlowGal: REAL;
    StatsDtS     : REAL;
END_VAR
VAR
    meter101 : RuntimeMeter;
    meter102 : RuntimeMeter;
END_VAR
meter101(Running := P101_Running, Dt := StatsDtS, Stats := P101_Stats);
meter102(Running := P102_Running, Dt := StatsDtS, Stats := P102_Stats);

(* Totalized discharge volume, gal: gal/min * s / 60. *)
StationFlowGal := StationFlowGal + FIT101_Flow * StatsDtS / 60.0;
END_PROGRAM
