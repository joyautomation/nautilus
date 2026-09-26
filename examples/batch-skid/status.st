PROGRAM Status
(* Batch UDT upkeep: mirrors the sequence's phase name into Batch.Phase,
   accumulates Batch.ElapsedSec since the batch left Idle, and stamps
   Line_BatchId onto a rising edge of Line_Request — the one non-BOOL
   "move on an edge" this project needs, which has no direct ladder rung
   form (coils only assign BOOL; see docs/languages/ladder.md), so it
   lives here instead of transfer.ld. Shared, unchanged, between the
   bench and line manifests. *)
VAR_EXTERNAL
    PhaseName    : STRING;
    Batch        : BatchStatus;
    StatusDtS    : REAL;
    Line_Request : BOOL;
    Line_BatchId : DINT;
    Running      : BOOL;
END_VAR
VAR
    running : BOOL;
    runEdge : R_TRIG;
    reqEdge : R_TRIG;
END_VAR

Batch.Phase := PhaseName;

running := NOT (PhaseName = 'Idle');
Running := running; (* Line_Fault's alarms.defs enable: reads this — see nautilus.yaml/line.yaml *)
runEdge(CLK := running);
IF runEdge.Q THEN
    Batch.ElapsedSec := 0.0;
END_IF;
IF running THEN
    Batch.ElapsedSec := Batch.ElapsedSec + StatusDtS;
END_IF;

reqEdge(CLK := Line_Request);
IF reqEdge.Q THEN
    Line_BatchId := Batch.Count;
END_IF;
END_PROGRAM
