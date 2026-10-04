PROGRAM Counters
(* CTU: counts on the pulse's rising edge, done at the preset, reset by R.
   Done is the counter's Q alone — it must stay true with no pulse present,
   which is where an inline XIC(c.DN) after the CTU would have been wrong. *)
VAR_EXTERNAL
    Pulse     : BOOL;
    Reset     : BOOL;
    Done      : BOOL;
    Armed     : BOOL;
    ArmedDone : BOOL;
END_VAR
VAR
    c  : CTU;
    c2 : CTU;
END_VAR
LD
  RUNG count (* three pulses *)
    Pulse c:CTU(PV := 3, R := Reset) ( Done )

  RUNG gated (* the pulse counts only while armed; Done follows the counter, not the arm *)
    Armed Pulse c2:CTU(PV := 2, R := Reset) ( ArmedDone )
END_LD
END_PROGRAM
