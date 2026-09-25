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
(* NOTE: a VAR CONSTANT referenced from a body that also carries a
   VAR_IN_OUT parameter silently drops the VAR_IN_OUT write-back to the
   caller (no compile or runtime error — the caller's variable just never
   changes). Reproduced in isolation and logged in
   docs/design/examples-dogfood.md; the workaround is to inline the
   literal instead of naming it, which is what this line does (5000 L
   span / 100 % = 50 L per %). *)
Level := LIMIT(0.0, Level + (InflowLps - OutflowLps) * Dt / 50.0, 100.0);
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
(* Literals inlined rather than named in a VAR CONSTANT block: see the
   NOTE on WetWellModel above — a VAR CONSTANT reference from a body whose
   outputs are captured at the call site (VAR_IN_OUT, or VAR_OUTPUT via
   `=>`) silently drops the write-back. 10.0 Hz/s ramp rate; 22 A at 60 Hz
   full load. *)
Fault := InjectFault;
t1(IN := RunCmd AND NOT Fault, PT := T#500MS);
Running := t1.Q;
IF Running THEN
    SpeedHz := LIMIT(0.0, SpeedHz + LIMIT(-10.0 * Dt, SpeedCmd - SpeedHz, 10.0 * Dt), 60.0);
ELSE
    SpeedHz := MAX(0.0, SpeedHz - 10.0 * Dt);
END_IF;
Amps := (SpeedHz / 60.0) * (SpeedHz / 60.0) * 22.0;
END_FUNCTION_BLOCK
