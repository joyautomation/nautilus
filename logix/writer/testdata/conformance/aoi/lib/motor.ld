(* Motor starter library: HOA (Off/Hand/Auto) selection, fail-to-run
   supervision, and a lockout on repeated fail-to-run trips — one block,
   instantiated once per pump in permissives.ld. Written as rungs (docs/functions.md "Function
   blocks in ladder"); a .ld file with no PROGRAM is a project library,
   exactly like a PROGRAM-less .st file. *)

FUNCTION_BLOCK MotorStarter
VAR_INPUT
    Mode       : INT;  (* 0 Off, 1 Hand, 2 Auto *)
    AutoReq    : BOOL; (* called by the sequence/HH override; meaningful in Auto *)
    Permissive : BOOL; (* seal / over-temp / VFD fault / LSLL all clear *)
    RunFb      : BOOL; (* VFD running feedback *)
    Reset      : BOOL; (* pulse: always clears FailToRun; clears the trip count/LockedOut too, but only once LockedOut is actually latched — see the lockout rung below *)
END_VAR
VAR_OUTPUT
    Run       : BOOL;
    InHand    : BOOL;
    FailToRun : BOOL;
    LockedOut : BOOL; (* three fail-to-run trips since the last Reset *)
END_VAR
VAR
    t1 : TON; (* fail-to-run: commanded but not confirmed running for 5 s *)
    c1 : CTU; (* counts FailToRun trips; three latches LockedOut until Reset *)
END_VAR
LD
  RUNG run (* Auto only calls when the sequence/HH override asks; Hand always runs *)
    [ EQ(Mode, 2) AutoReq | EQ(Mode, 1) ] Permissive /FailToRun /LockedOut ( Run )

  RUNG fail (* commanded to run, no running feedback for 5 s: failed to start *)
    Run /RunFb t1:TON(PT := T#5S) ( S FailToRun )

  RUNG reset
    Reset ( R FailToRun )

  RUNG hand
    EQ(Mode, 1) ( InHand )

  // one CTU count per fail-to-run trip; three locks the pump out. The
  // counter's own reset is Reset AND LockedOut, not Reset alone: every
  // ordinary fail-to-run retry pulses Reset just to clear FailToRun and
  // try again, and if that same pulse also zeroed the trip count, three
  // trips could never accumulate — the count would always be back to 0
  // before the next attempt even starts (see docs/design/examples-
  // dogfood.md for the failed-3-trips test that caught this). Gating the
  // counter's reset on LockedOut itself means it only clears once the
  // lockout has actually latched, which is also the one time "clears on
  // Reset" needs to be true.
  RUNG lockout
    +FailToRun c1:CTU(R := AND(Reset, LockedOut), PV := 3, Q => LockedOut)
END_LD
END_FUNCTION_BLOCK
