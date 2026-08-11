PROGRAM Sim
(* Stands in for the field bus: writes the Motor structs that a real
   deployment's EtherNet/IP driver would supply.

   NOTE THE SHAPE. You cannot write `P101.Running := ...` when P101 is a
   VAR_EXTERNAL — the VM has no store path for a field of a global, and it
   faults every scan (see the note in the walkthrough). The working form is
   copy → mutate the LOCAL → assign the whole struct back. Reading
   `P101.Running` is fine; only writing needs this. *)
VAR_EXTERNAL
    P101 : Motor;  P101_Enable : BOOL;  P101_StartSP : REAL;
    P102 : Motor;  P102_Enable : BOOL;  P102_StartSP : REAL;
    P103 : Motor;  P103_Enable : BOOL;  P103_StartSP : REAL;
    P201 : Motor;  P201_Enable : BOOL;  P201_StartSP : REAL;
    P202 : Motor;  P202_Enable : BOOL;  P202_StartSP : REAL;
    P301 : Motor;  P301_Enable : BOOL;  P301_StartSP : REAL;
END_VAR
VAR
    m : Motor;
END_VAR

m := P101; m.Running := P101_Enable AND NOT m.Fault;
IF m.Running THEN m.Speed := P101_StartSP; ELSE m.Speed := 0.0; END_IF; P101 := m;

m := P102; m.Running := P102_Enable AND NOT m.Fault;
IF m.Running THEN m.Speed := P102_StartSP; ELSE m.Speed := 0.0; END_IF; P102 := m;

m := P103; m.Running := P103_Enable AND NOT m.Fault;
IF m.Running THEN m.Speed := P103_StartSP; ELSE m.Speed := 0.0; END_IF; P103 := m;

m := P201; m.Running := P201_Enable AND NOT m.Fault;
IF m.Running THEN m.Speed := P201_StartSP; ELSE m.Speed := 0.0; END_IF; P201 := m;

m := P202; m.Running := P202_Enable AND NOT m.Fault;
IF m.Running THEN m.Speed := P202_StartSP; ELSE m.Speed := 0.0; END_IF; P202 := m;

m := P301; m.Running := P301_Enable AND NOT m.Fault;
IF m.Running THEN m.Speed := P301_StartSP; ELSE m.Speed := 0.0; END_IF; P301 := m;

END_PROGRAM
