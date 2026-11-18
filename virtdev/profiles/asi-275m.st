FUNCTION_BLOCK ASI_275M_SIM
(* A 16-in/16-out relay board: the controller writes one word of relay
   outputs and reads one word of inputs, point n on bit n-1. The relays and
   inputs are hooks for the process model. *)
VAR_INPUT
  Outputs : WORD;
  Input1 : BOOL; Input2 : BOOL; Input3 : BOOL; Input4 : BOOL;
  Input5 : BOOL; Input6 : BOOL; Input7 : BOOL; Input8 : BOOL;
  Input9 : BOOL; Input10 : BOOL; Input11 : BOOL; Input12 : BOOL;
  Input13 : BOOL; Input14 : BOOL; Input15 : BOOL; Input16 : BOOL;
END_VAR
VAR_OUTPUT
  Inputs : WORD;
  Relay1 : BOOL; Relay2 : BOOL; Relay3 : BOOL; Relay4 : BOOL;
  Relay5 : BOOL; Relay6 : BOOL; Relay7 : BOOL; Relay8 : BOOL;
  Relay9 : BOOL; Relay10 : BOOL; Relay11 : BOOL; Relay12 : BOOL;
  Relay13 : BOOL; Relay14 : BOOL; Relay15 : BOOL; Relay16 : BOOL;
END_VAR
Relay1 := Outputs.0;   Relay2 := Outputs.1;   Relay3 := Outputs.2;   Relay4 := Outputs.3;
Relay5 := Outputs.4;   Relay6 := Outputs.5;   Relay7 := Outputs.6;   Relay8 := Outputs.7;
Relay9 := Outputs.8;   Relay10 := Outputs.9;  Relay11 := Outputs.10; Relay12 := Outputs.11;
Relay13 := Outputs.12; Relay14 := Outputs.13; Relay15 := Outputs.14; Relay16 := Outputs.15;
Inputs := 0;
Inputs.0 := Input1;   Inputs.1 := Input2;   Inputs.2 := Input3;   Inputs.3 := Input4;
Inputs.4 := Input5;   Inputs.5 := Input6;   Inputs.6 := Input7;   Inputs.7 := Input8;
Inputs.8 := Input9;   Inputs.9 := Input10;  Inputs.10 := Input11; Inputs.11 := Input12;
Inputs.12 := Input13; Inputs.13 := Input14; Inputs.14 := Input15; Inputs.15 := Input16;
END_FUNCTION_BLOCK
