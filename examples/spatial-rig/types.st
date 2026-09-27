(* One UDT per kind of physical prop on the rig. A 3D or AR scene binds a
   whole asset by its tag name (T101, P101, XV101) and reads the members,
   so these structs are the contract between the rig and the scene file.
   A root .st with no PROGRAM is a library: `type: Tank` in the manifest
   resolves against it. *)

TYPE
  Tank : STRUCT
    Level : REAL;   (* % *)
    TempC : REAL;
    HH    : BOOL;   (* limit bits the alarm rules claim *)
    LL    : BOOL;
  END_STRUCT;

  Motor : STRUCT
    Run   : BOOL;   (* command *)
    Running : BOOL; (* feedback *)
    Fault : BOOL;
    Speed : REAL;   (* % *)
    Hours : REAL;
  END_STRUCT;

  Valve : STRUCT
    Cmd    : REAL;  (* % open command *)
    Pos    : REAL;  (* % open feedback *)
    Fault  : BOOL;  (* position disagrees with command *)
  END_STRUCT;
END_TYPE
