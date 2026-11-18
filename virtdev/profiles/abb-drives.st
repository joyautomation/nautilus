FUNCTION_BLOCK ABB_DRIVES_SIM
(* An ABB drive under the ABB Drives communication profile, as the FENA /
   FMBT-21 manual (3AUA0000093568 Rev E, pp. 91-96) describes it: the
   control word walks the state machine of p. 95, reference 1 (+-20000 =
   +-100 % of the speed scaling) sets the speed through a linear ramp, and
   the status word reports the state. Simplified: one ramp time, no
   supervision limit (ABOVE_LIMIT stays 0), Actual 2 reads 0, and the drive
   is always in remote (REMOTE = 1). *)
VAR_INPUT
  ControlWord : WORD;
  Reference1 : INT;
  Dt : REAL;              (* seconds since the last scan; the plant wires it *)
  Trip : BOOL;            (* hook: the process model trips the drive *)
  RunEnableLost : BOOL;   (* hook: the external Run enable is off *)
  RampS : REAL;           (* hook: seconds from zero to full speed; 0 means 5 *)
  NominalAmps : REAL;     (* hook: motor current at full load; 0 means 10 *)
  Load : REAL;            (* hook: 0..1, the load the process puts on the motor *)
END_VAR
VAR_OUTPUT
  StatusWord : WORD;
  Actual1 : INT;
  Actual2 : INT;
  Current : REAL;
  Running : BOOL;         (* hook: the motor turns *)
  SpeedPct : REAL;        (* hook: speed in % of the speed scaling *)
END_VAR
VAR
  State : INT;            (* 0 SWITCH-ON INHIBITED, 1 NOT READY TO SWITCH ON,
                             2 READY TO SWITCH ON, 3 READY TO OPERATE,
                             4 OPERATION ENABLED, 9 FAULT *)
  Speed : REAL;
  Fault : BOOL;
  PrevReset : BOOL;
  off1 : BOOL; off2 : BOOL; off3 : BOOL; enable : BOOL; remote : BOOL;
  reset : BOOL; rfgOut : BOOL; rfgHold : BOOL; rfgIn : BOOL;
  ramp : REAL; amps : REAL; target : REAL; step : REAL;
END_VAR
off1 := NOT ControlWord.0;
off2 := NOT ControlWord.1;
off3 := NOT ControlWord.2;
enable := ControlWord.3;
rfgOut := ControlWord.4;
rfgHold := ControlWord.5;
rfgIn := ControlWord.6;
reset := ControlWord.7 AND NOT PrevReset;
PrevReset := ControlWord.7;
remote := ControlWord.10;
ramp := RampS;
IF ramp <= 0.0 THEN ramp := 5.0; END_IF;
amps := NominalAmps;
IF amps <= 0.0 THEN amps := 10.0; END_IF;

(* FAULT from any state; RESET (a rising edge of bit 7) clears it once the
   cause is gone, and the drive goes to SWITCH-ON INHIBITED. *)
IF Trip THEN
  Fault := TRUE;
END_IF;
IF Fault AND reset AND NOT Trip THEN
  Fault := FALSE;
  State := 0;
END_IF;

IF Fault THEN
  State := 9;
ELSIF off2 OR off3 THEN
  State := 0;
ELSE
  CASE State OF
    0: IF off1 THEN State := 1; END_IF;
    1: IF off1 AND remote THEN State := 2; END_IF;
    2: IF NOT off1 AND remote THEN State := 3; END_IF;
    3: IF off1 THEN
         State := 2;
       ELSIF enable AND NOT RunEnableLost THEN
         State := 4;
       END_IF;
    4: IF off1 THEN
         State := 2;
       ELSIF NOT enable OR RunEnableLost THEN
         State := 3;
       END_IF;
  END_CASE;
END_IF;

(* The ramp function generator. *)
target := 0.0;
IF State = 4 AND rfgOut AND rfgIn AND remote THEN
  target := INT_TO_REAL(Reference1);
END_IF;
step := 20000.0 * Dt / ramp;
IF Fault OR off2 THEN
  Speed := 0.0;
ELSIF off3 THEN
  step := step * 10.0;
END_IF;
IF State = 4 AND NOT rfgHold THEN
  step := 0.0;
END_IF;
IF Speed < target THEN
  Speed := MIN(Speed + step, target);
ELSIF Speed > target THEN
  Speed := MAX(Speed - step, target);
END_IF;

Running := ABS(Speed) > 1.0;
SpeedPct := Speed / 200.0;
Actual1 := REAL_TO_INT(Speed);
Actual2 := 0;
IF Running THEN
  Current := amps * (0.3 + 0.7 * Load) * ABS(Speed) / 20000.0;
ELSE
  Current := 0.0;
END_IF;

StatusWord := 0;
StatusWord.0 := State >= 2 AND State <= 4;
StatusWord.1 := State >= 3 AND State <= 4;
StatusWord.2 := State = 4;
StatusWord.3 := Fault;
StatusWord.4 := NOT off2;
StatusWord.5 := NOT off3;
StatusWord.6 := State = 0;
StatusWord.8 := State = 4 AND ABS(Speed - target) <= 2000.0;
StatusWord.9 := TRUE;
StatusWord.11 := ControlWord.11;
StatusWord.12 := NOT RunEnableLost;
END_FUNCTION_BLOCK
