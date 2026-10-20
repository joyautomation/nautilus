(* The UDTs a well site is made of. Each site asset is ONE struct tag, so
   the scene binds a whole asset by name (WL101, R101) and reads its
   members. A root .st with no PROGRAM is a library: `type: Well` in the
   manifest resolves against it. *)

TYPE
  Motor : STRUCT
    Run     : BOOL;   (* command *)
    Running : BOOL;   (* feedback *)
    Fault   : BOOL;
    Speed   : REAL;   (* % *)
    Hours   : REAL;
  END_STRUCT;

  (* A production well: the submersible pump, and the water in the casing.
     Depths are metres below grade, so a bigger number is less water. *)
  Well : STRUCT
    Pump   : Motor;
    Depth  : REAL;    (* pumping water level, m below grade *)
    Static : REAL;    (* resting water level, m below grade *)
    Flow   : REAL;    (* at the wellhead, L/s *)
    (* the alarm rules' bits — rules match one level of members, so the
       pump's fault is mirrored here *)
    LL     : BOOL;    (* drawn down past the limit *)
    Fault  : BOOL;    (* = Pump.Fault *)
  END_STRUCT;

  Meter : STRUCT
    Flow  : REAL;     (* L/s *)
    Total : REAL;     (* m3 *)
  END_STRUCT;

  Valve : STRUCT
    Cmd   : REAL;     (* % open command *)
    Pos   : REAL;     (* % open feedback *)
    Fault : BOOL;     (* position disagrees with command *)
  END_STRUCT;

  Tank : STRUCT
    Level : REAL;     (* % *)
    TempC : REAL;
    HH    : BOOL;
    LL    : BOOL;
  END_STRUCT;

  (* Hypochlorite feed: a day tank and a dosing pump, as a UDT of UDTs,
     so the scene places the skid as one node and its parts read
     CL101.Tank and CL101.Pump (docs/design/spatial-hmi.md §3d). *)
  ChemFeed : STRUCT
    Tank     : Tank;
    Pump     : Motor;
    Residual : REAL;  (* mg/L free chlorine at the injection point *)
    Low      : BOOL;  (* residual under the limit *)
    Fault    : BOOL;  (* = Pump.Fault *)
  END_STRUCT;
END_TYPE
