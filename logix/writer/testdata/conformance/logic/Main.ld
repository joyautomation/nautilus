PROGRAM Logic
(* Series, parallel, nested branches, compares and their negation, latches,
   and several coils on one rung. *)
VAR_EXTERNAL
    A     : BOOL;
    B     : BOOL;
    C     : BOOL;
    X     : REAL;
    Y     : INT;
    AndOut   : BOOL;
    OrOut    : BOOL;
    Nest  : BOOL;
    Cmp   : BOOL;
    Lat   : BOOL;
    Both1 : BOOL;
    Both2 : BOOL;
END_VAR
LD
  RUNG and   A B ( AndOut )
  RUNG or    [ A | B ] /C ( OrOut )
  RUNG nest  [ A [ B | C ] | /A C ] ( Nest )
  RUNG cmp   GT(X, 10.0) /EQ(Y, 3) ( Cmp )
  RUNG set   A B C ( S Lat )
  RUNG reset /A /B /C ( R Lat )
  RUNG both  A ( Both1 ) ( Both2 )
END_LD
END_PROGRAM
