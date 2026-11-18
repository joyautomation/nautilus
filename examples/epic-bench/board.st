PROGRAM Board
(* sim.yaml only: the four field wires on the bench, output to input. A
   lamp the program switches on lights the input wired to it, as on the
   desk: DO3 to DI6, DO4 to DI8, DO5 to DI10, DO6 to DI12. bench.st
   mirrors DI6 back out on DO6, so DI3 (or a force on DO3) runs the whole
   chain to DI12. Every other input is a setpoint in tags/sim.yaml. On the
   processor (nautilus.yaml) this task does not run; the wires do it. *)
VAR_EXTERNAL
    EPIC_M1_DO03 : BOOL;  EPIC_M1_DO04 : BOOL;  EPIC_M1_DO05 : BOOL;  EPIC_M1_DO06 : BOOL;
    EPIC_M0_DI06 : BOOL;  EPIC_M0_DI08 : BOOL;  EPIC_M0_DI10 : BOOL;  EPIC_M0_DI12 : BOOL;
END_VAR

EPIC_M0_DI06 := EPIC_M1_DO03;
EPIC_M0_DI08 := EPIC_M1_DO04;
EPIC_M0_DI10 := EPIC_M1_DO05;
EPIC_M0_DI12 := EPIC_M1_DO06;
END_PROGRAM
