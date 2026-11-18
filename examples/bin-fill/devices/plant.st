PROGRAM Plant
(* The process: the gate (relay 1) opens after 1 s and shows on input 1;
   grain flows while the gate is open and the conveyor runs, 5 %/s; the
   high-level switch makes at 90 %. Carrying load raises the current. *)
VAR
  gateTmr : TON;
  level : REAL;
END_VAR
gateTmr(IN := RB1_Relay1, PT := T#1S);
RB1_Input1 := gateTmr.Q;
IF gateTmr.Q AND CONV_Running THEN
  level := level + 5.0 * Model_Dt;
END_IF;
IOLM_Port1_SioInput := level >= 90.0;
IF gateTmr.Q THEN
  CONV_Load := 1.0;
ELSE
  CONV_Load := 0.2;
END_IF;
END_PROGRAM
