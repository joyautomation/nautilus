(* A root .st file with no PROGRAM is a library: it is in scope for every
   task's compile, so `type: Motor` in the manifest resolves against it.
   On an EtherNet/IP project you do not write this file — `nautilus eip
   import` emits eip_types.st from the controller's own UDT definitions. *)

TYPE
  Motor : STRUCT
    Running : BOOL;
    Fault   : BOOL;
    Speed   : REAL;
    Hours   : REAL;
  END_STRUCT;
END_TYPE
