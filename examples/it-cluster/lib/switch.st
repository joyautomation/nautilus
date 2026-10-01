(* The switch half of the plant: fault inputs, and how they bend a port.

   A fault is an INPUT, never a value: the recording stays the source of
   every member no fault touches, and faults compose (a port can be
   speed-degraded AND erroring, with its switch dark over all of it).
   Clear the input and the recording shows through again. Write them from
   the HMI (Set Live Value), `curl .../api/tags`, or a test's given:. *)

TYPE
  SimPort : STRUCT
    Down      : BOOL; (* link lost: OperUp false, no traffic; admin stays up, so the monitor alarms *)
    AdminDown : BOOL; (* shut by an operator: AdminUp and OperUp false, not an alarm *)
    SpeedMbps : REAL; (* negotiated speed override, 0 = as recorded (10000 to 1000: degraded) *)
    ErrorRate : REAL; (* extra errors per second on top of the recording *)
    (* VLANs, applied in plant.st over the configured membership: *)
    Pvid      : DINT; (* native VLAN override, 0 = as configured (a native mismatch) *)
    Prune     : DINT; (* a VLAN taken off the port, 0 = none (a trunk missing a VLAN) *)
    Access    : DINT; (* the port moved to this access VLAN alone, 0 = as configured *)
  END_STRUCT;
  SimNet : STRUCT
    (* the layer-2 domain, all three switches trunked on their ring: a storm
       floods every port that has link *)
    BroadcastPps : REAL; (* a broadcast storm: packets per second flooded out of every up port *)
    MulticastPps : REAL; (* a multicast flood, the same way (no IGMP snooping to stop it) *)
  END_STRUCT;
  SimSwitch : STRUCT
    Dark   : BOOL; (* the switch stops answering: every port down, the agent silent *)
    Reboot : BOOL; (* a pulse: dark for RebootS, then back with its uptime reset; clears itself *)
  END_STRUCT;
END_TYPE

FUNCTION_BLOCK PortPlant
(* One switch port. P is the port as the device reports it: its identity
   (Name, Index, Alias, counters) is its own and never touched here; the
   members the recording carries follow Rec while the replay is Live, and
   hold the tag's initial snapshot while it is not (no recording loaded),
   so a plant without its data still reports a plausible, quiet switch. *)
VAR_INPUT
  Rec  : SwitchPort; (* the recording, from the replay driver *)
  F    : SimPort;    (* this port's faults *)
  Dark : BOOL;       (* the switch's *)
  Live : BOOL;       (* the recording is playing *)
  LinkOk : BOOL := TRUE; (* its cable, from both ends' faults (plant.st); TRUE unwired *)
  Net  : SimNet;     (* a storm in the layer-2 domain *)
  Dt   : REAL;       (* seconds since the last scan, for the discard counter *)
END_VAR
VAR_IN_OUT
  P : SwitchPort;
END_VAR
VAR
  B        : SwitchPort; (* the baseline: recording or snapshot, never a fault *)
  Init     : BOOL;
  FloodBps : REAL;
  LineBps  : REAL;
  Dropped  : REAL; (* discards not yet whole, so the counter never loses a fraction *)
END_VAR
VAR CONSTANT
  FloodBytes : REAL := 128.0; (* a storm's frames are small: ARP, discovery, a looped packet *)
END_VAR
IF NOT Init THEN
  B := P;
  Init := TRUE;
END_IF;
IF Live THEN
  B.AdminUp := Rec.AdminUp;
  B.OperUp := Rec.OperUp;
  B.SpeedMbps := Rec.SpeedMbps;
  B.InBps := Rec.InBps;
  B.OutBps := Rec.OutBps;
  B.ErrorRate := Rec.ErrorRate;
END_IF;
P.AdminUp := B.AdminUp AND NOT F.AdminDown;
P.OperUp := B.OperUp AND LinkOk AND NOT (F.Down OR F.AdminDown OR Dark);
P.SpeedMbps := B.SpeedMbps;
P.InBps := B.InBps;
P.OutBps := B.OutBps;
IF F.SpeedMbps > 0.0 AND P.OperUp THEN
  P.SpeedMbps := F.SpeedMbps;
  (* traffic cannot exceed the line *)
  P.InBps := MIN(P.InBps, F.SpeedMbps * 1000000.0);
  P.OutBps := MIN(P.OutBps, F.SpeedMbps * 1000000.0);
END_IF;
IF NOT P.OperUp THEN
  P.InBps := 0.0;
  P.OutBps := 0.0;
  P.InBroadcastPps := 0.0;
  P.InMulticastPps := 0.0;
ELSE
  (* a storm: every up port receives the flood and sends it on; what the
     line cannot carry is dropped, and counted *)
  P.InBroadcastPps := B.InBroadcastPps + Net.BroadcastPps;
  P.InMulticastPps := B.InMulticastPps + Net.MulticastPps;
  FloodBps := (Net.BroadcastPps + Net.MulticastPps) * FloodBytes * 8.0;
  LineBps := P.SpeedMbps * 1000000.0;
  P.InBps := MIN(P.InBps + FloodBps, LineBps);
  P.OutBps := P.OutBps + FloodBps;
  IF LineBps > 0.0 AND P.OutBps > LineBps THEN
    Dropped := Dropped + (P.OutBps - LineBps) / (FloodBytes * 8.0) * Dt;
    P.OutBps := LineBps;
  END_IF;
  IF Dropped >= 1.0 THEN
    P.OutDiscards := P.OutDiscards + REAL_TO_INT(Dropped - 0.5);
    Dropped := Dropped - INT_TO_REAL(REAL_TO_INT(Dropped - 0.5));
  END_IF;
END_IF;
P.ErrorRate := B.ErrorRate + F.ErrorRate;
P.Down := P.AdminUp AND NOT P.OperUp;
IF P.SpeedMbps > 0.0 THEN
  P.InPct := 100.0 * P.InBps / (P.SpeedMbps * 1000000.0);
  P.OutPct := 100.0 * P.OutBps / (P.SpeedMbps * 1000000.0);
ELSE
  P.InPct := 0.0;
  P.OutPct := 0.0;
END_IF;
END_FUNCTION_BLOCK

FUNCTION_BLOCK SwitchPlant
(* The chassis: dark, rebooting, and its uptime, which counts while it is
   up and starts again from 0 after a reboot. *)
VAR_INPUT
  Dt : REAL;
END_VAR
VAR_IN_OUT
  F : SimSwitch;
  P : Switch;
END_VAR
VAR_OUTPUT
  Down : BOOL; (* dark or rebooting: its ports and their cables are down *)
END_VAR
VAR
  T         : TON;
  Rebooting : BOOL;
  Up        : REAL;
  Init      : BOOL;
END_VAR
VAR CONSTANT
  RebootS : TIME := T#90S;
END_VAR
IF NOT Init THEN
  Up := INT_TO_REAL(P.UptimeS);
  Init := TRUE;
END_IF;
IF F.Reboot THEN
  Rebooting := TRUE;
  F.Reboot := FALSE;
END_IF;
T(IN := Rebooting, PT := RebootS);
IF T.Q THEN
  Rebooting := FALSE;
  Up := 0.0;
END_IF;
Down := F.Dark OR Rebooting;
IF NOT Down THEN
  Up := Up + Dt;
END_IF;
P.Online := NOT Down;
P.UptimeS := REAL_TO_INT(Up);
END_FUNCTION_BLOCK
