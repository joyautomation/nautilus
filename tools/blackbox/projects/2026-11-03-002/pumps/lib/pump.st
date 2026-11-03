(* Types and blocks for the duplex pump station. *)
TYPE
    PumpSel : (Pump1, Pump2) := Pump1;
END_TYPE

TYPE
    StationState : (Idle, LeadRunning, BothRunning) := Idle;
END_TYPE

TYPE
    PumpStats : STRUCT
        Starts : DINT;   (* rising edges of the run command *)
        Hours  : REAL;   (* accumulated run hours *)
    END_STRUCT;
END_TYPE

FUNCTION_BLOCK RunHours
(* Counts starts and accumulates run hours while Running. *)
VAR_INPUT
    Running : BOOL;
    Dt      : REAL;  (* seconds since the last call *)
END_VAR
VAR_IN_OUT
    Stats : PumpStats;
END_VAR
VAR
    startEdge : R_TRIG;
END_VAR
startEdge(CLK := Running);
IF startEdge.Q THEN
    Stats.Starts := Stats.Starts + 1;
END_IF;
IF Running THEN
    Stats.Hours := Stats.Hours + Dt / 3600.0;
END_IF;
END_FUNCTION_BLOCK
