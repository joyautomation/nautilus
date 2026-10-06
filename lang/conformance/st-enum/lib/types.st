(* An IEC enumerated type (#238): members with explicit values, and an
   initial member. A value shows by NAME in the tag table, /api/state and
   the editors; the integer is what a field bus carries. *)
TYPE
    WashState : (Idle, Fill := 10, Wash := 20, Drain := 30) := Idle;
    Valve     : (Closed, Open);
END_TYPE
