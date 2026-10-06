PROGRAM ImplicitST
(* Manifest tags used without VAR_EXTERNAL, in every place ST names a
   variable: an IF condition (spelled in another case), an assignment
   target, a member base, a CASE selector, a FOR body, a block input and an
   output binding. HiAlm keeps its declaration. *)
VAR_EXTERNAL
    HiAlm : BOOL;
END_VAR
VAR
    i : DINT;
    t : TON;
END_VAR

IF level > HiSP THEN
    HiAlm := TRUE;
ELSIF Level < HiSP - 10.0 THEN
    HiAlm := FALSE;
END_IF;

Pump.Speed := Level * 2.0;
Pump.Running := HiAlm;

CASE Cmd OF
    0: State := Idle;
    1: State := MachineState#Running;
    2: State := Fault;
END_CASE;
StateNo := TO_DINT(State);

Count := 0;
FOR i := 1 TO 3 DO
    Count := Count + i;
END_FOR;

t(IN := Run, PT := T#2S, Q => Delayed);
END_PROGRAM
