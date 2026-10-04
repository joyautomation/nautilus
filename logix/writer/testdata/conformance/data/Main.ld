PROGRAM Main
VAR_EXTERNAL
    Go     : BOOL;
    Step   : BOOL;
    Count  : DINT;
    Total  : DINT;
    Span   : REAL;
    Raw    : REAL;
    Scaled : REAL;
    Mag    : REAL;
    Mode   : INT;
    Hi     : BOOL;
    SetA   : DINT;
    SetB   : DINT;
    Armed  : BOOL;
    Seen   : BOOL;
END_VAR
LD
  RUNG count (* one ADD per rising edge of Step, while Go *)
    Go +Step { Count := Count + 1 }

  RUNG scale (* a CPT: percent of span, and an ABS *)
    Go { Scaled := Raw / Span * 100.0; Mag := ABS(Raw) }

  RUNG total (* a sum through MUL and SUB *)
    Go { Total := Count * 10 - 5 }

  RUNG cmp (* a compare with an expression operand *)
    GT(Scaled + 1.0, 50.0) ( Hi )

  RUNG modes (* boxes in parallel legs, each gated by its own condition *)
    Go [ EQ(Mode, 1) { SetA := 1 } | EQ(Mode, 2) { SetB := 2 } ]

  RUNG arm (* a one-shot of a compound condition *)
    Go GE(Count, 3) ( P Armed )

  RUNG seen (* a one-scan pulse is invisible to a 100 ms poll: latch it *)
    Armed ( S Seen )

  RUNG clear (* MOVE of a literal, when Go drops *)
    /Go { Count := 0; Total := 0; SetA := 0; SetB := 0 } ( R Seen )
END_LD
END_PROGRAM
