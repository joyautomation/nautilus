FUNCTION_BLOCK MCT4_SIM
(* An FDC MCT4's loop 1 (MCT4 4.3 User Manual Rev. G, App. A.1): a PI loop
   driving a burner's firing rate, over a first-order thermal model. Heat
   enters only while the burner manager reports a flame. In manual (Manual
   Override bit 0) the output is what the controller wrote to %Out. *)
VAR_INPUT
  AlarmReset : UINT;
  ManualOverride : WORD;
  SystemEvents : WORD;
  Loop1SP : INT;
  Dt : REAL;
  FlameOn : BOOL;       (* hook: the burner manager has a flame *)
  SensorBreak : BOOL;   (* hook: the loop's thermocouple breaks *)
  Ambient : REAL;       (* hook: temperature with no heat; 0 means 70 *)
  HeatRate : REAL;      (* hook: degrees per second at 100 % with a flame; 0 means 5 *)
  LossRate : REAL;      (* hook: share of (PV - ambient) lost per second; 0 means 0.01 *)
END_VAR
VAR_IN_OUT
  Loop1Out : REAL;      (* %Out: the controller's in manual, the loop's in auto *)
END_VAR
VAR_OUTPUT
  OfflineBusy : UINT;
  LoopErrorStatus : WORD;
  AlarmStatus1 : WORD;
  Loop1PV : INT;
  Loop1Status : WORD;
  Loop1Error : UINT;
  Temperature : REAL;   (* hook: the modelled temperature *)
END_VAR
VAR
  pv : REAL;
  started : BOOL;
  integ : REAL;
  e : REAL;
  amb : REAL;
  heat : REAL;
  loss : REAL;
  manual : BOOL;
END_VAR
amb := Ambient;
IF amb = 0.0 THEN amb := 70.0; END_IF;
heat := HeatRate;
IF heat <= 0.0 THEN heat := 5.0; END_IF;
loss := LossRate;
IF loss <= 0.0 THEN loss := 0.01; END_IF;
IF NOT started THEN
  pv := amb;
  started := TRUE;
END_IF;

manual := ManualOverride.0;
IF manual THEN
  Loop1Out := LIMIT(0.0, Loop1Out, 100.0);
  integ := Loop1Out;   (* bumpless back to auto *)
ELSIF SensorBreak THEN
  Loop1Out := 0.0;     (* no measurement, no heat *)
ELSE
  e := INT_TO_REAL(Loop1SP) - pv;
  integ := LIMIT(0.0, integ + 0.2 * e * Dt, 100.0);
  Loop1Out := LIMIT(0.0, 4.0 * e + integ, 100.0);
END_IF;

IF FlameOn THEN
  pv := pv + heat * Loop1Out / 100.0 * Dt;
END_IF;
pv := pv - loss * (pv - amb) * Dt;

Temperature := pv;
Loop1PV := REAL_TO_INT(pv);
Loop1Error := 0;
IF SensorBreak THEN
  Loop1Error := 39;
END_IF;
LoopErrorStatus := 0;
LoopErrorStatus.0 := SensorBreak;
Loop1Status := 0;
Loop1Status.2 := NOT manual;   (* Static Mode: no automatic program *)
Loop1Status.4 := manual;       (* Manual Mode *)
AlarmStatus1 := 0;
OfflineBusy := 0;
END_FUNCTION_BLOCK
