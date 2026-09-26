(* Bench-only plant model: a wet-well level integrator and a submersible
   pump/VFD dynamic model. Two FUNCTION_BLOCKs, no PROGRAM — a project
   library (docs/functions.md "A .ld library", the same rule for .st).
   sim.st is the only caller; field.yaml has no sim task, so these two
   blocks sit compiled but unused in that manifest — harmless dead code,
   the same way a project's other library functions are. *)

FUNCTION_BLOCK WetWellModel
(* Integrates a level from an inflow/outflow balance. WW-101: 4.0 m level
   span over 5 m3 of usable volume between the span's limits, so 1 L/s
   net moves the level 1 % in 50 s (5000 L / 100 % = 50 L per %). *)
VAR_INPUT
    InflowLps  : REAL;
    OutflowLps : REAL;
    Dt         : REAL; (* seconds *)
END_VAR
VAR_IN_OUT
    Level : REAL; (* %, 0..100 *)
END_VAR
VAR CONSTANT
    LPerPercent : REAL := 50.0; (* 5000 L span / 100 % = 50 L per % *)
END_VAR
Level := LIMIT(0.0, Level + (InflowLps - OutflowLps) * Dt / LPerPercent, 100.0);
END_FUNCTION_BLOCK

FUNCTION_BLOCK VfdModel
(* A submersible pump + VFD, standing in for the field device on the
   bench: RunCmd/SpeedCmd in, Running/SpeedHz/Amps/Fault out. Running lags
   RunCmd by 0.5 s (starter contactor + soft-start), and drops the instant
   a fault is injected. SpeedHz ramps toward SpeedCmd at 10 Hz/s while
   commanded and running, and ramps back to 0 otherwise. Amps follows
   speed-squared, the affinity-law shape a centrifugal load actually
   draws. Fault is a plain pass-through of the bench inject tag — a real
   VFD's own fault relay, not the ladder's fail-to-run latch (that is a
   separate, higher-level condition computed in permissives.ld). *)
VAR_INPUT
    RunCmd      : BOOL;
    SpeedCmd    : REAL;
    Dt          : REAL;
    InjectFault : BOOL;
END_VAR
VAR_OUTPUT
    Running : BOOL;
    SpeedHz : REAL;
    Amps    : REAL;
    Fault   : BOOL;
END_VAR
VAR
    t1 : TON;
END_VAR
VAR CONSTANT
    RampHzPerS : REAL := 10.0; (* speed ramp rate *)
    FullLoadA  : REAL := 22.0; (* amps at MaxHz, full load *)
    MaxHz      : REAL := 60.0; (* VFD speed ceiling *)
END_VAR
Fault := InjectFault;
t1(IN := RunCmd AND NOT Fault, PT := T#500MS);
Running := t1.Q;
IF Running THEN
    SpeedHz := LIMIT(0.0, SpeedHz + LIMIT(-RampHzPerS * Dt, SpeedCmd - SpeedHz, RampHzPerS * Dt), MaxHz);
ELSE
    SpeedHz := MAX(0.0, SpeedHz - RampHzPerS * Dt);
END_IF;
Amps := (SpeedHz / MaxHz) * (SpeedHz / MaxHz) * FullLoadA;
END_FUNCTION_BLOCK
