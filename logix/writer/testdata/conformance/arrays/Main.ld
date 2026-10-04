PROGRAM Main
VAR_EXTERNAL
    Go      : BOOL;
    Sel     : DINT;
    Done0   : BOOL;
    Done1   : BOOL;
    DoneSel : BOOL;
    Hits    : DINT;
END_VAR
VAR
    Timers : ARRAY [0..3] OF TON;
    Counts : ARRAY [0..1] OF CTU;
END_VAR
LD
  RUNG t0 (* element 0: half a second *)
    Go Timers[0]:TON(PT := T#500MS) ( Done0 )

  RUNG t1 (* element 1: two seconds *)
    Go Timers[1]:TON(PT := T#2S) ( Done1 )

  RUNG tsel (* an element picked at run time *)
    Go Timers[Sel]:TON(PT := T#1S)

  RUNG dsel
    Timers[Sel].Q ( DoneSel )

  RUNG count (* a counter in an array, counting the first timer's done edges *)
    +Done0 Counts[1]:CTU(PV := 10, R := NOT(Go))

  RUNG hits
    Go { Hits := Counts[1].CV }
END_LD
END_PROGRAM
