(* A slower task: writes single bits of a shared word and, sometimes,
   the whole word; and names its tags with no VAR_EXTERNAL (implicit
   scope). *)
PROGRAM Slow
VAR
  i : INT;
END_VAR
Status.3 := Level > 10.0;
IF Stop THEN
  Status.0 := TRUE;
END_IF;
IF Start THEN
  Status := 16#0;
END_IF;
FOR i := 1 TO 3 DO
  Status.7 := NOT Status.7;
END_FOR;
END_PROGRAM
