PROGRAM Main
VAR_EXTERNAL
  Release : DINT;
  Scans : DINT;
  SP : REAL; (* the retained setpoint the check writes; nothing reads it *)
END_VAR
VAR CONSTANT
  THIS_RELEASE : DINT := 1;
END_VAR
Release := THIS_RELEASE;
Scans := Scans + 1;
END_PROGRAM
