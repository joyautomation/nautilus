(* MotorStarter — the add-on instruction an AB programmer writes on day
   one, as a ladder FUNCTION_BLOCK: start/stop seal-in, a fail-to-start
   timer on the contactor's auxiliary feedback, and a latched fault that
   only a Reset (with the overload cleared) releases. Call it once per
   motor from the main routine; each call is its own instance (the AOI's
   backing tag), with its own timer. *)

FUNCTION_BLOCK MotorStarter
VAR_INPUT
    Start  : BOOL; (* start request — the caller one-shots the pushbutton *)
    Stop   : BOOL; (* stop pushbutton, TRUE while pressed *)
    Permit : BOOL; (* interlocks healthy *)
    Aux    : BOOL; (* contactor auxiliary contact (running feedback) *)
    Fault  : BOOL; (* overload / drive fault *)
    Reset  : BOOL; (* fault reset pushbutton *)
END_VAR
VAR_OUTPUT
    Run         : BOOL; (* contactor command *)
    FailToStart : BOOL; (* commanded, no aux feedback within 3 s *)
    Faulted     : BOOL; (* latched: overload or fail-to-start *)
END_VAR
LD
  RUNG run (* seal-in: Start energizes Run, Run holds itself in *)
    [ Start | Run ] /Stop Permit /Faulted ( Run )

  RUNG fts (* commanded but the aux never made: fail to start *)
    Run /Aux tFail:TON(PT := T#3S) ( S FailToStart )

  RUNG flt
    [ Fault | FailToStart ] ( S Faulted )

  RUNG rst (* reset only once the overload has cleared *)
    Reset /Fault ( R Faulted ) ( R FailToStart )
END_LD
END_FUNCTION_BLOCK
