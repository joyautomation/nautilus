(* The main task: a seal-in on a shared tag (assigned every scan, often
   to the value it has), a struct member, a timer output and a latch bound
   to shared tags, a tag no other task names, and a RETURN. *)
PROGRAM Main
VAR_EXTERNAL
  Start : BOOL;
  Stop : BOOL;
  Run : BOOL;
  Pump : Motor;
  Level : REAL;
  Done : BOOL;
  Alarm : BOOL;
  Status : WORD;
  Local1 : DINT;
END_VAR
VAR
  dwell : TON;
  latch : SR;
  Name : INT;
END_VAR
Run := (Start OR Run) AND NOT Stop;
Pump.Run := Run AND Level < 90.0;
dwell(IN := Run, PT := T#5s, Q => Done);
latch(S1 := Level > 95.0, R := Stop, Q1 => Alarm);
Local1 := Local1 + 1;
Name := Name + 1;
IF NOT Status.3 THEN
  RETURN;
END_IF;
Pump.Speed := 0.0;
END_PROGRAM
