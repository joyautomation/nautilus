PROGRAM Fill
(* Start the conveyor (ABB Drives profile: 0x0476 until READY TO SWITCH ON,
   then 0x047F OPERATING at full speed), open the gate once the drive is at
   speed, close it at the high level, run the conveyor empty for 3 s, stop.
   A plug or a drive trip stops everything.

   Idle holds the control word at 0. The Modbus driver records a project's
   first output push as its start-up baseline and never sends it, so a
   0x0476 written on the first scan would never reach the drive, and a
   drive that never sees bit 0 low stays SWITCH-ON INHIBITED. *)
VAR
  purge : TON;
END_VAR
GateOpen := RB_In.0;
CASE Step OF
  0: (* idle *)
    Conv_CW := 0;
    Conv_Ref := 0;
    RB_Out := 0;
    IF StartReq AND NOT BinHigh THEN
      Step := 1;
    END_IF;
  1: (* conveyor starting: READY TO SWITCH ON first, then OPERATING *)
    IF Conv_SW.0 OR Conv_SW.1 THEN
      Conv_CW := 16#047F;
    ELSE
      Conv_CW := 16#0476;
    END_IF;
    Conv_Ref := 20000;
    IF Conv_SW.2 AND Conv_SW.8 THEN   (* RDY_REF and AT_SETPOINT *)
      Step := 2;
    END_IF;
  2: (* filling *)
    RB_Out := 1;
    IF BinHigh OR StopReq THEN
      RB_Out := 0;
      Step := 3;
    END_IF;
  3: (* purging the conveyor *)
    IF purge.Q THEN
      Conv_CW := 16#0476;
      Conv_Ref := 0;
      Step := 4;
    END_IF;
  4: (* done *)
    IF NOT StartReq THEN
      Step := 0;
    END_IF;
END_CASE;
IF Plugged OR Conv_SW.3 THEN   (* plug switch, or TRIPPED *)
  Conv_CW := 16#0476;
  Conv_Ref := 0;
  RB_Out := 0;
  Step := 9;
END_IF;
purge(IN := Step = 3, PT := T#3S);
END_PROGRAM
