(* A root .st file with no PROGRAM is a project library, in scope for
   every task's compile — see docs/functions.md "A .ld library" (the same
   rule, one language over). Pump is the SAME shape scada/sites.yaml
   declares as its shared Template for all three sites, but this copy, not
   a shared package, is what ships: each site deploys alone. *)
TYPE
  Pump : STRUCT
    Run     : BOOL;   (* running feedback *)
    Fault   : BOOL;   (* VFD fault *)
    SpeedHz : LREAL;  (* actual speed *)
    SpeedSP : LREAL;  (* operator/SCADA setpoint — written locally or by
                         a Sparkplug NCMD member write from the SCADA host *)
  END_STRUCT;
END_TYPE
