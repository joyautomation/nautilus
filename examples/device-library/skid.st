PROGRAM Skid
(* Open the inlet valve (relay 1), then start the pump (ABB Drives profile:
   0x0476 until READY TO SWITCH ON, then 0x047F OPERATING) at half speed.
   Stop closes the valve after the pump is stopped. A drive trip stops
   everything and holds Step at 9 until the operator clears the request.

   Idle writes the control word 0 on the first scan: the driver keeps a
   project's first output push as its start-up baseline and never sends
   it, so a 0x0476 written then would never reach the drive. *)
CASE Step OF
  0: (* idle *)
    PUMP_ControlWord := 0;
    PUMP_Reference1 := 0;
    VALVE_Relay1 := FALSE;
    IF StartReq AND NOT PUMP.Tripped THEN
      Step := 1;
    END_IF;
  1: (* valve opening: the relay is closed, wait for it to read back *)
    VALVE_Relay1 := TRUE;
    IF VALVE.Relay1 THEN
      Step := 2;
    END_IF;
  2: (* pump starting *)
    IF PUMP.StatusWord.0 OR PUMP.StatusWord.1 THEN
      PUMP_ControlWord := 16#047F;
    ELSE
      PUMP_ControlWord := 16#0476;
    END_IF;
    PUMP_Reference1 := 10000;
    IF PUMP.StatusWord.2 AND PUMP.StatusWord.8 THEN   (* RDY_REF and AT_SETPOINT *)
      Step := 3;
    END_IF;
  3: (* running *)
    IF StopReq OR NOT StartReq THEN
      PUMP_ControlWord := 16#0476;
      PUMP_Reference1 := 0;
      IF PUMP.Actual1 = 0 THEN
        VALVE_Relay1 := FALSE;
        Step := 0;
      END_IF;
    END_IF;
END_CASE;
IF PUMP.Tripped THEN
  PUMP_ControlWord := 16#0476;
  PUMP_Reference1 := 0;
  VALVE_Relay1 := FALSE;
  Step := 9;
END_IF;
IF Step = 9 AND NOT StartReq AND NOT PUMP.Tripped THEN
  Step := 0;
END_IF;
END_PROGRAM
