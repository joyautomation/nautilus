(* scenarios.st: named scenarios, so a demo is one write:
     curl -X POST localhost:8087/api/tags -d '{"name":"Scenario","value":"cpu-overheat"}'
   A scenario only SETS fault inputs; the inputs stay the source of truth.
   So scenarios compose (fan-fail, then hot-room, is both), any input can
   still be written by hand on top, and "normal" clears every fault.
   Add a scenario here: a name, and the inputs it sets. *)
PROGRAM Scenarios
VAR_EXTERNAL
  Scenario    : STRING;
  ClearFaults : BOOL;
  Sim_Room    : SimRoom;
  Sim_SW1_Port25 : SimPort; Sim_SW2_Port25 : SimPort; Sim_SW2_Port26 : SimPort;
  Sim_SW3 : SimSwitch;
  Sim_Cable_SW3_Port25 : SimCable; Sim_Cable_NODE1_Nic_Slot2_P1 : SimCable;
  Sim_NODE1_Fan3 : SimFan; Sim_NODE1_PSU2 : SimPSU;
  Sim_NODE2 : SimServer; Sim_NODE2_Fan3 : SimFan; Sim_NODE2_Fan4 : SimFan;
  Sim_NODE3 : SimServer; Sim_NODE3_Drive_NVMe1 : SimDrive; Sim_NODE3_Drive_NVMe2 : SimDrive;
END_VAR
VAR
  Last    : STRING;
  Unknown : BOOL; (* the last name written is not a scenario *)
END_VAR
IF Scenario <> Last THEN
  Last := Scenario;
  Unknown := FALSE;
  IF Scenario = 'normal' THEN
    ClearFaults := TRUE;
  ELSIF Scenario = 'port-down' THEN
    (* a switch port loses link: SW2 te0/26, node1's slot 3 port 2 at the far end *)
    Sim_SW2_Port26.Down := TRUE;
  ELSIF Scenario = 'cable-pull' THEN
    (* the cable between SW3 te0/25 and node3 slot 3 port 2 *)
    Sim_Cable_SW3_Port25.Pulled := TRUE;
  ELSIF Scenario = 'mesh-cable-pull' THEN
    (* a server-to-server link: node1 slot 2 port 1 to node2 slot 3 port 1 *)
    Sim_Cable_NODE1_Nic_Slot2_P1.Pulled := TRUE;
  ELSIF Scenario = 'speed-degrade' THEN
    (* node1's uplink renegotiates at 1G *)
    Sim_SW1_Port25.SpeedMbps := 1000.0;
  ELSIF Scenario = 'error-burst' THEN
    Sim_SW2_Port25.ErrorRate := 50.0;
  ELSIF Scenario = 'switch-down' THEN
    Sim_SW3.Dark := TRUE;
  ELSIF Scenario = 'switch-reboot' THEN
    Sim_SW3.Reboot := TRUE;
  ELSIF Scenario = 'cpu-overheat' THEN
    (* flat out with two fans gone: the CPU climbs past HighHigh in a few minutes *)
    Sim_NODE2.CpuLoad := 100.0;
    Sim_NODE2_Fan3.Fail := TRUE;
    Sim_NODE2_Fan4.Fail := TRUE;
  ELSIF Scenario = 'fan-fail' THEN
    Sim_NODE1_Fan3.Fail := TRUE;
  ELSIF Scenario = 'psu-loss' THEN
    Sim_NODE1_PSU2.InputLost := TRUE;
  ELSIF Scenario = 'drive-pull' THEN
    Sim_NODE3_Drive_NVMe2.Pulled := TRUE;
  ELSIF Scenario = 'drive-failing' THEN
    Sim_NODE3_Drive_NVMe1.Failing := TRUE;
  ELSIF Scenario = 'hot-room' THEN
    Sim_Room.InletDeltaC := 15.0;
  ELSIF Scenario = 'node-off' THEN
    Sim_NODE2.PowerOff := TRUE;
  ELSIF Scenario = 'bmc-dark' THEN
    Sim_NODE3.Dark := TRUE;
  ELSE
    Unknown := TRUE;
  END_IF;
END_IF;
END_PROGRAM
