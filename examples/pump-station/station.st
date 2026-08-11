PROGRAM Station
(* Per-pump logic. This is the part the design brief is honest about NOT
   shortening: the manifest holds 24 tags from 9 authored rows, but the
   VAR_EXTERNAL block below is still one line per tag. `nautilus eip import`
   emits a ready-to-paste block for exactly this reason. *)
VAR_EXTERNAL
    P101 : Motor;  P101_RunHrs : REAL;  P101_StartSP : REAL;  P101_Enable : BOOL;
    P102 : Motor;  P102_RunHrs : REAL;  P102_StartSP : REAL;  P102_Enable : BOOL;
    P103 : Motor;  P103_RunHrs : REAL;  P103_StartSP : REAL;  P103_Enable : BOOL;
    P201 : Motor;  P201_RunHrs : REAL;  P201_StartSP : REAL;  P201_Enable : BOOL;
    P202 : Motor;  P202_RunHrs : REAL;  P202_StartSP : REAL;  P202_Enable : BOOL;
    P301 : Motor;  P301_RunHrs : REAL;  P301_StartSP : REAL;  P301_Enable : BOOL;
    StationDtS  : REAL;
    RunningCount : REAL;
    AnyFault     : BOOL;
END_VAR

(* run-hour accumulation, per pump *)
IF P101.Running THEN P101_RunHrs := P101_RunHrs + StationDtS / 3600.0; END_IF;
IF P102.Running THEN P102_RunHrs := P102_RunHrs + StationDtS / 3600.0; END_IF;
IF P103.Running THEN P103_RunHrs := P103_RunHrs + StationDtS / 3600.0; END_IF;
IF P201.Running THEN P201_RunHrs := P201_RunHrs + StationDtS / 3600.0; END_IF;
IF P202.Running THEN P202_RunHrs := P202_RunHrs + StationDtS / 3600.0; END_IF;
IF P301.Running THEN P301_RunHrs := P301_RunHrs + StationDtS / 3600.0; END_IF;

(* station rollups — the tags an operator screen actually shows *)
RunningCount := 0.0;
IF P101.Running THEN RunningCount := RunningCount + 1.0; END_IF;
IF P102.Running THEN RunningCount := RunningCount + 1.0; END_IF;
IF P103.Running THEN RunningCount := RunningCount + 1.0; END_IF;
IF P201.Running THEN RunningCount := RunningCount + 1.0; END_IF;
IF P202.Running THEN RunningCount := RunningCount + 1.0; END_IF;
IF P301.Running THEN RunningCount := RunningCount + 1.0; END_IF;

AnyFault := P101.Fault OR P102.Fault OR P103.Fault
         OR P201.Fault OR P202.Fault OR P301.Fault;

END_PROGRAM
