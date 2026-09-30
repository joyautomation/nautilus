(* scenarios.st: compiled by gen.py from scenarios.yaml. Do not edit:
   add or change a scenario there. Writing Scenario starts it: a set: at
   once, a timeline step by step (Phase, seconds from the write, wrapping
   every repeat:). "normal" clears every fault. *)
PROGRAM Scenarios
VAR_EXTERNAL
  Scenario : STRING; ClearFaults : BOOL; ScenarioUnknown : BOOL;
  ScenarioCatalog : STRING; (* for the scenario panel: every name and what it does *)
  ScenarioS : REAL; ScenarioDtS : REAL;
  Sim_Cable_NODE1_Nic_Slot2_P1 : SimCable;
  Sim_Cable_SW3_Port25 : SimCable;
  Sim_NODE1_Fan3 : SimFan;
  Sim_NODE1_PSU2 : SimPSU;
  Sim_NODE2 : SimServer;
  Sim_NODE2_Fan3 : SimFan;
  Sim_NODE2_Fan4 : SimFan;
  Sim_NODE3 : SimServer;
  Sim_NODE3_Drive_NVMe1 : SimDrive;
  Sim_NODE3_Drive_NVMe2 : SimDrive;
  Sim_Net : SimNet;
  Sim_Room : SimRoom;
  Sim_SW1_Port25 : SimPort;
  Sim_SW2_Port25 : SimPort;
  Sim_SW2_Port26 : SimPort;
  Sim_SW3 : SimSwitch;
END_VAR
VAR
  Last   : STRING;
  Active : INT;  (* the scenario running its timeline; 0 none *)
  Phase  : REAL; (* seconds into its cycle *)
  S1_1 : BOOL;
  S2_1 : BOOL;
  S2_2 : BOOL;
  S3_1 : BOOL;
  S4_1 : BOOL;
  S5_1 : BOOL;
  S6_1 : BOOL;
  S7_1 : BOOL;
  S8_1 : BOOL;
  S9_1 : BOOL;
  S10_1 : BOOL;
  S11_1 : BOOL;
  S12_1 : BOOL;
  S13_1 : BOOL;
  S14_1 : BOOL;
  S15_1 : BOOL;
  S16_1 : BOOL;
  S17_1 : BOOL;
  S18_1 : BOOL;
  S18_2 : BOOL;
  S19_1 : BOOL;
  S20_1 : BOOL;
  S20_2 : BOOL;
  S20_3 : BOOL;
  S20_4 : BOOL;
  S20_5 : BOOL;
  S20_6 : BOOL;
  S20_7 : BOOL;
  S20_8 : BOOL;
  S20_9 : BOOL;
  S20_10 : BOOL;
  S20_11 : BOOL;
END_VAR
IF Scenario <> Last THEN
  Last := Scenario;
  ScenarioS := 0.0;
  Phase := 0.0;
  ScenarioUnknown := FALSE;
    S1_1 := FALSE;
    S2_1 := FALSE;
    S2_2 := FALSE;
    S3_1 := FALSE;
    S4_1 := FALSE;
    S5_1 := FALSE;
    S6_1 := FALSE;
    S7_1 := FALSE;
    S8_1 := FALSE;
    S9_1 := FALSE;
    S10_1 := FALSE;
    S11_1 := FALSE;
    S12_1 := FALSE;
    S13_1 := FALSE;
    S14_1 := FALSE;
    S15_1 := FALSE;
    S16_1 := FALSE;
    S17_1 := FALSE;
    S18_1 := FALSE;
    S18_2 := FALSE;
    S19_1 := FALSE;
    S20_1 := FALSE;
    S20_2 := FALSE;
    S20_3 := FALSE;
    S20_4 := FALSE;
    S20_5 := FALSE;
    S20_6 := FALSE;
    S20_7 := FALSE;
    S20_8 := FALSE;
    S20_9 := FALSE;
    S20_10 := FALSE;
    S20_11 := FALSE;
  IF Scenario = 'normal' THEN
    Active := 0;
    ClearFaults := TRUE;
  ELSIF Scenario = 'port-down' THEN
    Active := 1;
  ELSIF Scenario = 'port-flap' THEN
    Active := 2;
  ELSIF Scenario = 'cable-pull' THEN
    Active := 3;
  ELSIF Scenario = 'mesh-cable-pull' THEN
    Active := 4;
  ELSIF Scenario = 'speed-degrade' THEN
    Active := 5;
  ELSIF Scenario = 'error-burst' THEN
    Active := 6;
  ELSIF Scenario = 'broadcast-storm' THEN
    Active := 7;
  ELSIF Scenario = 'multicast-storm' THEN
    Active := 8;
  ELSIF Scenario = 'switch-down' THEN
    Active := 9;
  ELSIF Scenario = 'switch-reboot' THEN
    Active := 10;
  ELSIF Scenario = 'cpu-overheat' THEN
    Active := 11;
  ELSIF Scenario = 'fan-fail' THEN
    Active := 12;
  ELSIF Scenario = 'psu-loss' THEN
    Active := 13;
  ELSIF Scenario = 'drive-pull' THEN
    Active := 14;
  ELSIF Scenario = 'drive-failing' THEN
    Active := 15;
  ELSIF Scenario = 'node-off' THEN
    Active := 16;
  ELSIF Scenario = 'bmc-dark' THEN
    Active := 17;
  ELSIF Scenario = 'bmc-reboot' THEN
    Active := 18;
  ELSIF Scenario = 'hot-room' THEN
    Active := 19;
  ELSIF Scenario = 'cooling-failure' THEN
    Active := 20;
  ELSE
    Active := 0;
    ScenarioUnknown := TRUE;
  END_IF;
END_IF;
  IF Active = 1 THEN (* port-down: A switch port loses link (SW2 te0/26); node1's slot 3 port 2, at the far end, loses it too. *)
    IF NOT S1_1 AND Phase >= 0.0 THEN Sim_SW2_Port26.Down := TRUE; S1_1 := TRUE; END_IF;
  ELSIF Active = 2 THEN (* port-flap: SW2 te0/26 flaps, down 10 s in every 30 s: never long enough for a link-down alarm. *)
    IF NOT S2_1 AND Phase >= 0.0 THEN Sim_SW2_Port26.Down := TRUE; S2_1 := TRUE; END_IF;
    IF NOT S2_2 AND Phase >= 10.0 THEN Sim_SW2_Port26.Down := FALSE; S2_2 := TRUE; END_IF;
    IF Phase >= 30.0 THEN Phase := Phase - 30.0; S2_1 := FALSE; S2_2 := FALSE; END_IF;
  ELSIF Active = 3 THEN (* cable-pull: The cable between SW3 te0/25 and node3's slot 3 port 2 is pulled. *)
    IF NOT S3_1 AND Phase >= 0.0 THEN Sim_Cable_SW3_Port25.Pulled := TRUE; S3_1 := TRUE; END_IF;
  ELSIF Active = 4 THEN (* mesh-cable-pull: A server-to-server link (node1 slot 2 port 1 to node2 slot 3 port 1) is pulled. *)
    IF NOT S4_1 AND Phase >= 0.0 THEN Sim_Cable_NODE1_Nic_Slot2_P1.Pulled := TRUE; S4_1 := TRUE; END_IF;
  ELSIF Active = 5 THEN (* speed-degrade: node1's uplink (SW1 te0/25) renegotiates at 1G. *)
    IF NOT S5_1 AND Phase >= 0.0 THEN Sim_SW1_Port25.SpeedMbps := 1000.0; S5_1 := TRUE; END_IF;
  ELSIF Active = 6 THEN (* error-burst: SW2 te0/25 starts counting 50 errors a second. *)
    IF NOT S6_1 AND Phase >= 0.0 THEN Sim_SW2_Port25.ErrorRate := 50.0; S6_1 := TRUE; END_IF;
  ELSIF Active = 7 THEN (* broadcast-storm: A broadcast storm (a loop, say) floods every port in the layer-2 domain; the 1G ports saturate and drop. *)
    IF NOT S7_1 AND Phase >= 0.0 THEN Sim_Net.BroadcastPps := 1000000.0; S7_1 := TRUE; END_IF;
  ELSIF Active = 8 THEN (* multicast-storm: A multicast flood with no IGMP snooping to stop it reaches every port. *)
    IF NOT S8_1 AND Phase >= 0.0 THEN Sim_Net.MulticastPps := 300000.0; S8_1 := TRUE; END_IF;
  ELSIF Active = 9 THEN (* switch-down: SW3 goes dark: its ports and every cable to it go down, and it stops answering. *)
    IF NOT S9_1 AND Phase >= 0.0 THEN Sim_SW3.Dark := TRUE; S9_1 := TRUE; END_IF;
  ELSIF Active = 10 THEN (* switch-reboot: SW3 reboots, dark for 90 s, and comes back with its uptime reset. *)
    IF NOT S10_1 AND Phase >= 0.0 THEN Sim_SW3.Reboot := TRUE; S10_1 := TRUE; END_IF;
  ELSIF Active = 11 THEN (* cpu-overheat: node2 runs flat out with two fans gone; its CPU climbs past HighHigh in a few minutes. *)
    IF NOT S11_1 AND Phase >= 0.0 THEN Sim_NODE2.CpuLoad := 100.0; Sim_NODE2_Fan3.Fail := TRUE; Sim_NODE2_Fan4.Fail := TRUE; S11_1 := TRUE; END_IF;
  ELSIF Active = 12 THEN (* fan-fail: node1's FAN3 stops; the others ramp and the CPU warms. *)
    IF NOT S12_1 AND Phase >= 0.0 THEN Sim_NODE1_Fan3.Fail := TRUE; S12_1 := TRUE; END_IF;
  ELSIF Active = 13 THEN (* psu-loss: node1's PSU2 loses its input; PSU1 carries the load and the server stays up. *)
    IF NOT S13_1 AND Phase >= 0.0 THEN Sim_NODE1_PSU2.InputLost := TRUE; S13_1 := TRUE; END_IF;
  ELSIF Active = 14 THEN (* drive-pull: node3's NVMe bay 2 is pulled. *)
    IF NOT S14_1 AND Phase >= 0.0 THEN Sim_NODE3_Drive_NVMe2.Pulled := TRUE; S14_1 := TRUE; END_IF;
  ELSIF Active = 15 THEN (* drive-failing: node3's NVMe bay 1 predicts its own failure. *)
    IF NOT S15_1 AND Phase >= 0.0 THEN Sim_NODE3_Drive_NVMe1.Failing := TRUE; S15_1 := TRUE; END_IF;
  ELSIF Active = 16 THEN (* node-off: node2 powers off; its links drop and the switches alarm. *)
    IF NOT S16_1 AND Phase >= 0.0 THEN Sim_NODE2.PowerOff := TRUE; S16_1 := TRUE; END_IF;
  ELSIF Active = 17 THEN (* bmc-dark: node3's BMC stops answering. *)
    IF NOT S17_1 AND Phase >= 0.0 THEN Sim_NODE3.Dark := TRUE; S17_1 := TRUE; END_IF;
  ELSIF Active = 18 THEN (* bmc-reboot: node3's BMC reboots, dark for two minutes, then back. *)
    IF NOT S18_1 AND Phase >= 0.0 THEN Sim_NODE3.Dark := TRUE; S18_1 := TRUE; END_IF;
    IF NOT S18_2 AND Phase >= 120.0 THEN Sim_NODE3.Dark := FALSE; S18_2 := TRUE; END_IF;
  ELSIF Active = 19 THEN (* hot-room: The room runs 15 °C hot; every inlet, and everything behind it, rises. *)
    IF NOT S19_1 AND Phase >= 0.0 THEN Sim_Room.InletDeltaC := 15.0; S19_1 := TRUE; END_IF;
  ELSIF Active = 20 THEN (* cooling-failure: The room's cooling fails; the inlets climb 2 °C a minute for ten minutes. *)
    IF NOT S20_1 AND Phase >= 0.0 THEN Sim_Room.InletDeltaC := 0.0; S20_1 := TRUE; END_IF;
    IF NOT S20_2 AND Phase >= 60.0 THEN Sim_Room.InletDeltaC := 2.0; S20_2 := TRUE; END_IF;
    IF NOT S20_3 AND Phase >= 120.0 THEN Sim_Room.InletDeltaC := 4.0; S20_3 := TRUE; END_IF;
    IF NOT S20_4 AND Phase >= 180.0 THEN Sim_Room.InletDeltaC := 6.0; S20_4 := TRUE; END_IF;
    IF NOT S20_5 AND Phase >= 240.0 THEN Sim_Room.InletDeltaC := 8.0; S20_5 := TRUE; END_IF;
    IF NOT S20_6 AND Phase >= 300.0 THEN Sim_Room.InletDeltaC := 10.0; S20_6 := TRUE; END_IF;
    IF NOT S20_7 AND Phase >= 360.0 THEN Sim_Room.InletDeltaC := 12.0; S20_7 := TRUE; END_IF;
    IF NOT S20_8 AND Phase >= 420.0 THEN Sim_Room.InletDeltaC := 14.0; S20_8 := TRUE; END_IF;
    IF NOT S20_9 AND Phase >= 480.0 THEN Sim_Room.InletDeltaC := 16.0; S20_9 := TRUE; END_IF;
    IF NOT S20_10 AND Phase >= 540.0 THEN Sim_Room.InletDeltaC := 18.0; S20_10 := TRUE; END_IF;
    IF NOT S20_11 AND Phase >= 600.0 THEN Sim_Room.InletDeltaC := 20.0; S20_11 := TRUE; END_IF;
  END_IF;
ScenarioS := ScenarioS + ScenarioDtS;
Phase := Phase + ScenarioDtS;
END_PROGRAM
