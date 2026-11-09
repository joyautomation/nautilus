PROGRAM Sealin
(* Start/stop seal-in: Start latches the motor on through its own
   contact; Stop breaks the rung. *)
LD
  RUNG sealin (* start, or already running, and not stopped *)
    [ StartPB | Motor ] /StopPB ( Motor )
END_LD
END_PROGRAM
