(* A faster task: writes one shared tag, and reads tags the main task
   writes through a timer's and a latch's output bindings. *)
PROGRAM Fast
VAR_EXTERNAL
  Pump : Motor;
  Level : REAL;
END_VAR
IF Pump.Run AND NOT (Done OR Alarm) THEN
  Level := Level + Pump.Speed * 0.01;
ELSE
  Level := Level - 0.5;
END_IF;
END_PROGRAM
