(* A structure and an enumeration for manifest tags to take as type:. On
   Logix the structure is a UDT; the enumeration is a DINT and its members
   their values (Idle 1, Running 10, Fault 11). *)
TYPE
  PumpData : STRUCT
    Speed   : REAL;
    Running : BOOL;
  END_STRUCT;
  MachineState : (Idle := 1, Running := 10, Fault);
END_TYPE
