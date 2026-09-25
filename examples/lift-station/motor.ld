(* Motor starter library: HOA (Off/Hand/Auto) selection, fail-to-run
   supervision, and a start counter — one block, instantiated once per
   pump in permissives.ld. Written as rungs (docs/functions.md "Function
   blocks in ladder"); a .ld file with no PROGRAM is a project library,
   exactly like a PROGRAM-less .st file. *)

FUNCTION_BLOCK MotorStarter
VAR_INPUT
    Mode       : INT;  (* 0 Off, 1 Hand, 2 Auto *)
    AutoReq    : BOOL; (* called by the sequence/HH override; meaningful in Auto *)
    Permissive : BOOL; (* seal / over-temp / VFD fault / LSLL all clear *)
    RunFb      : BOOL; (* VFD running feedback *)
    Reset      : BOOL; (* pulse: clears FailToRun and the start count *)
END_VAR
VAR_OUTPUT
    Run       : BOOL;
    InHand    : BOOL;
    FailToRun : BOOL;
END_VAR
VAR
    t1 : TON; (* fail-to-run: commanded but not confirmed running for 5 s *)
    c1 : CTU; (* start count, for the HMI — the retained UDT keeps the real tally *)
END_VAR
LD
  RUNG run (* Auto only calls when the sequence/HH override asks; Hand always runs *)
    [ EQ(Mode, 2) AutoReq | EQ(Mode, 1) ] Permissive /FailToRun ( Run )

  RUNG fail (* commanded to run, no running feedback for 5 s: failed to start *)
    Run /RunFb t1:TON(PT := T#5S) ( S FailToRun )

  RUNG reset
    Reset ( R FailToRun )

  RUNG hand
    EQ(Mode, 1) ( InHand )

  RUNG starts (* HMI-facing start pulse counter *)
    +Run c1:CTU(R := Reset, PV := 32000)
END_LD
END_FUNCTION_BLOCK
