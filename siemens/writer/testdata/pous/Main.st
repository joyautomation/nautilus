PROGRAM Main
(* Two instances of the library's Station, one bound to tags and one to a
   local; the library's FUNCTIONs in expressions, positional and formal. *)
VAR_EXTERNAL
    Run    : BOOL;
    Level  : REAL;
    Starts : INT;
    High   : BOOL;
    Pct    : REAL;
    Count  : DINT;
    Next   : INT;
END_VAR
VAR
    st1         : Station;
    st2         : Station;
    localStarts : INT;
END_VAR
st1(Run := Run, Level := Level, Starts := Starts, High => High, Count => Count);
st2(Run := NOT Run, Level := Scale(Level, 0.0, HI_LIMIT), Starts := localStarts);
Pct := Scale(Raw := Level, Lo := 0.0, Hi := 100.0);
Next := Bump(Starts) + st1.Total;
END_PROGRAM
