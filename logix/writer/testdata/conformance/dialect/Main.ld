PROGRAM Main
VAR_EXTERNAL
    Go     : BOOL;
    Clear  : BOOL;
    Pulses : DINT;
    Held   : BOOL;
END_VAR
VAR
    pulse : TONR;
    hold  : TONR;
END_VAR
LD
  RUNG free (* the free-running pulse: the timer resets itself when done *)
    Go pulse:TONR(PT := T#500MS, Reset := pulse.Q)

  RUNG count (* one count per pulse *)
    +pulse.Q { Pulses := Pulses + 1 }

  RUNG hold (* a timer an input resets while it runs *)
    Go hold:TONR(PT := T#1S, Reset := Clear) ( Held )

  RUNG clear
    /Go { Pulses := 0 }
END_LD
END_PROGRAM
