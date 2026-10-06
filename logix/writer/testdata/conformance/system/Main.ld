PROGRAM Main
VAR_EXTERNAL
    Restart : BOOL;
    Seeded : DINT;
    Ticks  : DINT;
    Year   : DINT;
    Month  : DINT;
    Day    : DINT;
    Hour   : DINT;
    Sec    : DINT;
END_VAR
VAR
    wallClock : LOCAL_TIME;
    gettime   : ARRAY[0..6] OF DINT;
    clk       : LOCAL_TIME;
    nextSec   : DINT;
END_VAR
LD
  RUNG clock (* the import's shape: one GSV into an array *)
    wallClock:LOCAL_TIME() { gettime[0] := wallClock.YEAR; gettime[1] := wallClock.MONTH; gettime[2] := wallClock.DAY; gettime[3] := wallClock.HOUR; gettime[4] := wallClock.MINUTE; gettime[5] := wallClock.SECOND; gettime[6] := wallClock.MILLISECOND * 1000 }

  RUNG seed (* the sequencer starts from the clock on the first scan *)
    FIRST_SCAN() { Seeded := Seeded + 1 } { nextSec := gettime[5] }

  RUNG tick (* one count per second *)
    NE(gettime[5], nextSec) { Ticks := Ticks + 1 } { nextSec := gettime[5] }

  RUNG restart (* the test's handle on the count *)
    Restart { Ticks := 0 }

  RUNG direct (* a clock read by member *)
    clk:LOCAL_TIME() { Year := clk.YEAR } { Month := clk.MONTH } { Day := clk.DAY } { Hour := clk.HOUR } { Sec := clk.SECOND }
END_LD
END_PROGRAM
