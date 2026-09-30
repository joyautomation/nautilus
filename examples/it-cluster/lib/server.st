(* The server half of the plant: fault inputs, a first-order thermal model,
   and how a server's parts respond. Same rules as lib/switch.st: a fault
   is an input, the recording is the baseline, faults compose, clearing a
   fault lets the recording show through again.

   The thermal model is deliberately small, and every number that shapes it
   is a named constant below, to tune against what a real box does:

     load rise    = KLoad * (CpuLoad - RecLoadPct)
     rise target  = load rise
                  + KFanFrac * failed fans * (CPU over inlet + load rise)
                  - KBoostCool * (FanBoost - 1)         the others ramping
     each sensor  = recorded + Weight * rise + room offset, approached
                    with time constant TauS (first order), so a failure
                    makes temperatures CLIMB rather than jump
     fan boost    = 1 + KBoost * (CPU rise now), capped at MaxBoost: the
                    BMC's fan curve answering the temperature it reads

   Where the numbers come from (the node is a Supermicro 1U with one
   Xeon 6505P: 150 W TDP, 97 °C DTS max; its BMC calls 102 °C critical):
     WPerPct   150 W TDP over an idle package of about 35 W, across 90 %
               of load: about 1.3 W per %
     KLoad     that power through a 1U heatsink at about 0.3 °C/W:
               about 0.35 °C per %
     KFanFrac  one of six fans gone costs the heatsink about a fifth of its
               airflow; its thermal resistance, and so the CPU's rise over
               the inlet, grows by about as much: small at idle, large
               under load, which is how a lost fan really behaves
     TauS      a 1U heatsink under forced air settles in about a minute
               (vendors give 60 s to swap a fan before cooling is lost)
     CapC      the CPU throttles near its limit rather than running away:
               held a little past the BMC's critical, so the alarm trips *)

TYPE
  SimServer : STRUCT
    Dark     : BOOL; (* the BMC stops answering: Online false *)
    PowerOff : BOOL; (* host off: standby power, fans stop, temps fall to inlet, links drop *)
    CpuLoad  : REAL; (* % busy, 0 = as recorded (the recording is a quiet cluster, about RecLoadPct) *)
  END_STRUCT;
  SimFan : STRUCT
    Fail : BOOL; (* stopped: RPM 0, Fault; the other fans ramp, temperatures rise *)
  END_STRUCT;
  SimPSU : STRUCT
    InputLost : BOOL; (* line cord out: InputOk false, output 0, the other PSU carries the load *)
    Fail      : BOOL; (* the supply itself fails: Fault, output 0 *)
  END_STRUCT;
  SimDrive : STRUCT
    Pulled  : BOOL; (* out of its bay: Present false *)
    Failing : BOOL; (* SMART predicts failure: PredictedFailure, health Warning *)
  END_STRUCT;
  SimNetPort : STRUCT
    Down : BOOL; (* the port loses link: both ends of its cable go down *)
  END_STRUCT;
  SimCable : STRUCT
    Pulled : BOOL; (* the cable itself: both ends down *)
  END_STRUCT;
  SimRoom : STRUCT
    InletDeltaC : REAL; (* the room runs hot: every server's inlet, and everything behind it, rises *)
  END_STRUCT;
END_TYPE

FUNCTION_BLOCK ServerPlant
(* The chassis: power, CPU busy, and the thermal model's rise. Its outputs
   drive the fans, sensors and PSUs of the same server. *)
VAR_INPUT
  Rec        : Server;
  F          : SimServer;
  Room       : SimRoom;
  FansFailed : DINT;
  CpuRiseNow : REAL; (* the CPU sensor's current rise over the recording, for the fan curve *)
  CpuOverInlet : REAL := 35.0; (* the recorded CPU temperature above the recorded inlet *)
  Live       : BOOL;
END_VAR
VAR_IN_OUT
  P : Server;
END_VAR
VAR_OUTPUT
  RiseTarget : REAL; (* °C the CPU heads for above the recording *)
  FanBoost   : REAL; (* the fans' speed factor *)
  Off        : BOOL;
  LoadW      : REAL; (* the power the PSUs deliver *)
END_VAR
VAR
  B        : Server;
  Init     : BOOL;
  LoadRise : REAL;
END_VAR
VAR CONSTANT
  RecLoadPct : REAL := 10.0;  (* the recording's CPU busy: an idle cluster *)
  KLoad      : REAL := 0.35;  (* °C per % of extra load *)
  KFanFrac   : REAL := 0.2;   (* per failed fan: the share by which the CPU's rise over the inlet grows *)
  KBoost     : REAL := 0.03;  (* fan speed-up per °C of CPU rise *)
  MaxBoost   : REAL := 1.6;   (* fans at their ceiling *)
  KBoostCool : REAL := 10.0;  (* °C the ramped fans take back at full boost - 1 *)
  WPerPct    : REAL := 1.3;   (* W per % of extra load *)
  StandbyW   : REAL := 12.0;  (* host off, BMC on *)
END_VAR
IF NOT Init THEN
  B := P;
  Init := TRUE;
END_IF;
IF Live THEN
  B.PowerOn := Rec.PowerOn;
  B.PowerW := Rec.PowerW;
  B.InletTempC := Rec.InletTempC;
  B.MaxTempC := Rec.MaxTempC;
END_IF;
Off := F.PowerOff OR NOT B.PowerOn;
P.Online := NOT F.Dark;
P.PowerOn := NOT Off;
FanBoost := LIMIT(1.0, 1.0 + KBoost * MAX(CpuRiseNow, 0.0), MaxBoost);
IF Off THEN
  RiseTarget := 0.0;
  LoadW := StandbyW;
  P.CpuPct := 0.0;
ELSE
  LoadRise := 0.0;
  IF F.CpuLoad > 0.0 THEN
    LoadRise := KLoad * (F.CpuLoad - RecLoadPct);
  END_IF;
  RiseTarget := LoadRise + KFanFrac * INT_TO_REAL(FansFailed) * MAX(CpuOverInlet + LoadRise, 0.0)
    - KBoostCool * (FanBoost - 1.0);
  IF F.CpuLoad > 0.0 THEN
    LoadW := B.PowerW + WPerPct * (F.CpuLoad - RecLoadPct);
    P.CpuPct := F.CpuLoad;
  ELSE
    LoadW := B.PowerW;
    P.CpuPct := RecLoadPct;
  END_IF;
END_IF;
P.PowerW := LoadW;
P.InletTempC := B.InletTempC + Room.InletDeltaC;
END_FUNCTION_BLOCK

FUNCTION_BLOCK TempPlant
(* One temperature sensor: its recording, plus Weight x the server's rise,
   plus the room; approached with TauS. Off, it cools to the inlet. *)
VAR_INPUT
  Rec        : TempSensor;
  RiseTarget : REAL;
  Weight     : REAL; (* 1 for the CPU, less for what sits further from it, 0 for the inlet *)
  RoomC      : REAL;
  InletC     : REAL; (* where it cools to, host off *)
  Off        : BOOL;
  Dt         : REAL;
  Live       : BOOL;
  CapC       : REAL := 110.0; (* where the part throttles or trips; the CPU's is lower *)
END_VAR
VAR_IN_OUT
  P : TempSensor;
END_VAR
VAR_OUTPUT
  Rise : REAL; (* now, over the recording *)
  Base : REAL; (* the recording (or snapshot) *)
END_VAR
VAR
  B     : TempSensor;
  Init  : BOOL;
  State : REAL; (* the sensor's own lagged offset from the recording *)
  Goal  : REAL;
END_VAR
VAR CONSTANT
  TauS    : REAL := 60.0;  (* heating and cooling time constant *)
  OffTauS : REAL := 300.0; (* cooling to the inlet, host off *)
END_VAR
IF NOT Init THEN
  B := P;
  Init := TRUE;
END_IF;
IF Live THEN
  B.Value := Rec.Value;
END_IF;
IF Off THEN
  Goal := InletC - B.Value;
  State := State + (Goal - State) * MIN(Dt / OffTauS, 1.0);
ELSE
  Goal := Weight * RiseTarget + RoomC;
  State := State + (Goal - State) * MIN(Dt / TauS, 1.0);
END_IF;
P.Value := MIN(B.Value + State, CapC);
Rise := P.Value - B.Value;
Base := B.Value;
P.High := P.HighSP > 0.0 AND P.Value >= P.HighSP;
P.HighHigh := P.HighHighSP > 0.0 AND P.Value >= P.HighHighSP;
END_FUNCTION_BLOCK

FUNCTION_BLOCK FanPlant
VAR_INPUT
  Rec   : Fan;
  F     : SimFan;
  Boost : REAL;
  Off   : BOOL;
  Live  : BOOL;
END_VAR
VAR_IN_OUT
  P : Fan;
END_VAR
VAR
  B    : Fan;
  Init : BOOL;
END_VAR
VAR CONSTANT
  MaxRPM : REAL := 16000.0;
END_VAR
IF NOT Init THEN
  B := P;
  Init := TRUE;
END_IF;
IF Live THEN
  B.RPM := Rec.RPM;
END_IF;
IF F.Fail OR Off THEN
  P.RPM := 0.0;
ELSE
  P.RPM := MIN(B.RPM * Boost, MaxRPM);
END_IF;
P.Fault := F.Fail;
END_FUNCTION_BLOCK

FUNCTION_BLOCK PsuPair
(* Two supplies sharing a load: each carries its recorded share while both
   are up; one lost, the other carries it all and the server stays up. *)
VAR_INPUT
  Rec1, Rec2 : PSU;
  F1, F2     : SimPSU;
  LoadW      : REAL;
  Live       : BOOL;
END_VAR
VAR_IN_OUT
  P1, P2 : PSU;
END_VAR
VAR
  B1, B2 : PSU;
  Init   : BOOL;
  Up1, Up2 : BOOL;
  Share1 : REAL;
END_VAR
IF NOT Init THEN
  B1 := P1;
  B2 := P2;
  Init := TRUE;
END_IF;
IF Live THEN
  B1.InputV := Rec1.InputV;
  B1.OutputW := Rec1.OutputW;
  B2.InputV := Rec2.InputV;
  B2.OutputW := Rec2.OutputW;
END_IF;
Up1 := NOT (F1.InputLost OR F1.Fail);
Up2 := NOT (F2.InputLost OR F2.Fail);
Share1 := 0.5;
IF B1.OutputW + B2.OutputW > 0.0 THEN
  Share1 := B1.OutputW / (B1.OutputW + B2.OutputW);
END_IF;
IF NOT Up2 THEN
  Share1 := 1.0;
ELSIF NOT Up1 THEN
  Share1 := 0.0;
END_IF;
P1.OutputW := LoadW * Share1;
P2.OutputW := LoadW * (1.0 - Share1);
P1.InputOk := NOT F1.InputLost;
P2.InputOk := NOT F2.InputLost;
P1.InputV := SEL(F1.InputLost, B1.InputV, 0.0);
P2.InputV := SEL(F2.InputLost, B2.InputV, 0.0);
(* a supply with no input reports itself unhealthy, as a real one does *)
P1.Fault := NOT Up1;
P2.Fault := NOT Up2;
END_FUNCTION_BLOCK

FUNCTION_BLOCK DrivePlant
VAR_INPUT
  F : SimDrive;
END_VAR
VAR_IN_OUT
  P : Drive;
END_VAR
P.Present := NOT F.Pulled;
P.PredictedFailure := F.Failing;
IF F.Failing THEN
  P.Health := 1;
ELSE
  P.Health := 0;
END_IF;
P.Fault := P.Health > 0 OR NOT P.Present;
END_FUNCTION_BLOCK

FUNCTION_BLOCK NicPlant
(* A server's network port. LinkOk is its cable's state, worked out once
   per cable from both ends' faults (plant.st), so a port down at the
   switch drops the server's end too: the cable goes dark at both ends. *)
VAR_INPUT
  Rec       : NetPort;
  LinkOk    : BOOL;
  SpeedGbps : REAL; (* the cable's negotiated speed override, 0 = as recorded *)
  Live      : BOOL;
END_VAR
VAR_IN_OUT
  P : NetPort;
END_VAR
VAR
  B    : NetPort;
  Init : BOOL;
END_VAR
IF NOT Init THEN
  B := P;
  Init := TRUE;
END_IF;
IF Live THEN
  B.LinkUp := Rec.LinkUp;
  B.SpeedGbps := Rec.SpeedGbps;
END_IF;
P.LinkUp := B.LinkUp AND LinkOk;
IF NOT P.LinkUp THEN
  P.SpeedGbps := 0.0;
ELSIF SpeedGbps > 0.0 THEN
  P.SpeedGbps := SpeedGbps;
ELSE
  P.SpeedGbps := B.SpeedGbps;
END_IF;
END_FUNCTION_BLOCK
