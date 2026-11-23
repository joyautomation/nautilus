(* Structured types (#1299): a tag of a nested STRUCT with start values,
   member arithmetic that wraps as nautilus stores it, a whole struct
   copied, a struct temp reset every scan, and a member SCL reserves. *)
TYPE
  Motor : STRUCT
    Run    : BOOL;
    Speed  : REAL := 50.0;
    Starts : INT;
  END_STRUCT;
  Well : STRUCT
    Pump   : Motor;
    Depth  : REAL;
    Static : REAL;
    Name   : DINT := 7;
  END_STRUCT;
END_TYPE

PROGRAM Main
VAR_EXTERNAL
  WL101 : Well;
  WL102 : Well;
END_VAR
VAR
  spare : Motor;
  last : Motor;
END_VAR
VAR_TEMP
  t : Motor;
END_VAR

IF WL101.Pump.Run AND NOT last.Run THEN
  WL101.Pump.Starts := WL101.Pump.Starts + 1;
END_IF;
last := WL101.Pump;
t.Speed := WL101.Pump.Speed * 2.0;
spare.Speed := t.Speed;
WL102.Static := WL101.Static;
WL102.Name := WL101.Name + 1;
END_PROGRAM
