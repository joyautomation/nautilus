(* Runtime accounting shared by both pumps: a retained UDT (PumpStats) and
   a FUNCTION_BLOCK that updates it by reference, so neither the sequence
   nor the permissives ever touch accumulation math themselves. A .st file
   with no PROGRAM is a project library — see docs/functions.md "A .ld
   library" (the same rule, one language over). *)

TYPE
  PumpStats : STRUCT
    Starts     : INT;   (* start count *)
    Hours      : REAL;  (* run hours *)
    LastRunSec : REAL;  (* length of the most recently completed run *)
  END_STRUCT;
END_TYPE

FUNCTION_BLOCK RuntimeMeter
(* Counts a start on Running's rising edge, accumulates run hours while
   Running, and captures the length of the run that just ended. Bound to
   the UDT tag by VAR_IN_OUT so two calls (P101_Stats, P102_Stats) each
   read and write their own copy — see docs/functions.md "VAR_IN_OUT is a
   reference pin". *)
VAR_INPUT
    Running : BOOL;
    Dt      : REAL; (* seconds since the last call *)
END_VAR
VAR_IN_OUT
    Stats : PumpStats;
END_VAR
VAR
    edgeUp   : R_TRIG;
    edgeDown : F_TRIG;
    runSec   : REAL;
END_VAR
edgeUp(CLK := Running);
edgeDown(CLK := Running);
IF edgeUp.Q THEN
    Stats.Starts := Stats.Starts + 1;
    runSec := 0.0;
END_IF;
IF Running THEN
    Stats.Hours := Stats.Hours + Dt / 3600.0;
    runSec := runSec + Dt;
END_IF;
IF edgeDown.Q THEN
    Stats.LastRunSec := runSec;
END_IF;
END_FUNCTION_BLOCK

FUNCTION PumpFlowLps : REAL
(* Discharge flow at a given VFD speed — a rough affinity curve pinned to
   two points: ~0 L/s below 25 Hz (not enough head to open the check
   valve), 25 L/s at 60 Hz, linear between. Shared by the bench sim
   (lib/physics.st) and anywhere logic wants an expected flow from a
   commanded speed. *)
VAR_INPUT
    SpeedHz : REAL;
END_VAR
IF SpeedHz <= 25.0 THEN
    PumpFlowLps := 0.0;
ELSE
    PumpFlowLps := (SpeedHz - 25.0) / (60.0 - 25.0) * 25.0;
END_IF;
END_FUNCTION
