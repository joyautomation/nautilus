(* The main task: integrates over its own dt-tag (a REAL), and reads the
   fast task's (an LREAL), which makes that one a shared tag. *)
PROGRAM Main
VAR_EXTERNAL
  MainDtS : REAL;
  Hours : REAL;
  FastDt : LREAL;
  Seen : LREAL;
END_VAR
Hours := Hours + MainDtS / 3600.0;
Seen := FastDt;
END_PROGRAM
