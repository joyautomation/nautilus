PROGRAM WetWell
(* Bench-only plant: WW-101 (level from an inflow/outflow balance, via
   lib/physics.st's WetWellModel) and two submersible-pump/VFD models
   (VfdModel). Inflow is InflowLps plus a slow sinusoid (+-20 %) so the
   level never sits dead flat; InflowLps set to 40 is the "storm" the
   design calls out. A real deployment deletes this task entirely —
   field.yaml has no sim task and points the same control programs at
   the real devices instead (see docs/design/examples-dogfood.md for the
   one place the field tag names needed a small glue program). *)
VAR_EXTERNAL
    LIT101_Level : REAL;
    LSHH101      : BOOL;
    LSLL101      : BOOL;
    FIT101_Flow  : REAL;
    SimDtS       : REAL;
    InflowLps    : REAL;
    ForceLSHH    : BOOL;

    P101_RunCmd      : BOOL;
    P101_SpeedCmd    : REAL;
    P101_Running     : BOOL;
    P101_SpeedHz     : REAL;
    P101_Amps        : REAL;
    P101_VfdFault    : BOOL;
    P101_InjectFault : BOOL;
    P101_SealFail        : BOOL;
    P101_InjectSealFail  : BOOL;
    P101_OverTemp        : BOOL;
    P101_InjectOverTemp  : BOOL;

    P102_RunCmd      : BOOL;
    P102_SpeedCmd    : REAL;
    P102_Running     : BOOL;
    P102_SpeedHz     : REAL;
    P102_Amps        : REAL;
    P102_VfdFault    : BOOL;
    P102_InjectFault : BOOL;
    P102_SealFail        : BOOL;
    P102_InjectSealFail  : BOOL;
    P102_OverTemp        : BOOL;
    P102_InjectOverTemp  : BOOL;
END_VAR
VAR
    well   : WetWellModel;
    vfd101 : VfdModel;
    vfd102 : VfdModel;
    tSec   : REAL;
END_VAR
VAR CONSTANT
    InflowVaryFrac : REAL := 0.20;
    InflowPeriodS  : REAL := 240.0;
    TwoPi          : REAL := 6.2831853;
END_VAR

tSec := tSec + SimDtS;

vfd101(RunCmd := P101_RunCmd, SpeedCmd := P101_SpeedCmd, Dt := SimDtS, InjectFault := P101_InjectFault,
       Running => P101_Running, SpeedHz => P101_SpeedHz, Amps => P101_Amps, Fault => P101_VfdFault);
vfd102(RunCmd := P102_RunCmd, SpeedCmd := P102_SpeedCmd, Dt := SimDtS, InjectFault := P102_InjectFault,
       Running => P102_Running, SpeedHz => P102_SpeedHz, Amps => P102_Amps, Fault => P102_VfdFault);

(* Seal-leak and over-temp probes have no physics to speak of: pass the
   bench inject tags straight through, exactly like a wire from a switch. *)
P101_SealFail := P101_InjectSealFail;
P101_OverTemp := P101_InjectOverTemp;
P102_SealFail := P102_InjectSealFail;
P102_OverTemp := P102_InjectOverTemp;

FIT101_Flow := PumpFlowLps(P101_SpeedHz) + PumpFlowLps(P102_SpeedHz);

well(InflowLps := InflowLps * (1.0 + InflowVaryFrac * SIN(tSec / InflowPeriodS * TwoPi)),
     OutflowLps := FIT101_Flow, Dt := SimDtS, Level := LIT101_Level);

IF ForceLSHH THEN
    LSHH101 := TRUE;
ELSE
    LSHH101 := LIT101_Level >= 95.0;
END_IF;
LSLL101 := LIT101_Level <= 8.0;
END_PROGRAM
