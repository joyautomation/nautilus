PROGRAM FTrigLd
(* F_TRIG in ladder: rung power drives CLK, Q continues to the coil. *)
VAR_EXTERNAL
    CLK  : BOOL;
    ld_Q : BOOL;
END_VAR
VAR
    t : F_TRIG;
END_VAR
LD
  RUNG ftrig
    CLK t:F_TRIG() ( ld_Q )
END_LD
END_PROGRAM
