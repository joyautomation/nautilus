(* rack.st — the rollups a rack needs that no single device carries: how
   many switch ports are up, whether anything in the rack is faulted, and
   the hottest thing in it. Everything else — every port's status, every
   fan's RPM, every sensor's thresholds — is already a UDT tag the drivers
   deliver, alarmed by the rules in nautilus.yaml, so this program stays
   small on purpose.

   Every driver-fed tag is read under its source's __Online companion: a
   device that stops answering keeps its last values (Stale) rather than
   dropping to zero, so the guard is what keeps a dark switch from being
   counted as 24 healthy ports. *)
PROGRAM Rack
VAR_EXTERNAL
  SW1 : Switch;
  SW1_Port01 : SwitchPort; SW1_Port02 : SwitchPort; SW1_Port03 : SwitchPort; SW1_Port04 : SwitchPort;
  SW1_Port05 : SwitchPort; SW1_Port06 : SwitchPort; SW1_Port07 : SwitchPort; SW1_Port08 : SwitchPort;
  SW1_Port09 : SwitchPort; SW1_Port10 : SwitchPort; SW1_Port11 : SwitchPort; SW1_Port12 : SwitchPort;
  SW1_Port13 : SwitchPort; SW1_Port14 : SwitchPort; SW1_Port15 : SwitchPort; SW1_Port16 : SwitchPort;
  SW1_Port17 : SwitchPort; SW1_Port18 : SwitchPort; SW1_Port19 : SwitchPort; SW1_Port20 : SwitchPort;
  SW1_Port21 : SwitchPort; SW1_Port22 : SwitchPort; SW1_Port23 : SwitchPort; SW1_Port24 : SwitchPort;
  SW1_Port25 : SwitchPort; SW1_Port26 : SwitchPort; SW1_Port27 : SwitchPort; SW1_Port28 : SwitchPort;
  SW1__Online : BOOL;
  MIRA1 : Server;
  MIRA1__Online : BOOL;

  PortsUp : INT;
  PortsDown : INT;
  DevicesOnline : INT;
  RackFault : BOOL;
  RackMaxTempC : REAL;
END_VAR
VAR
  up : INT;
  down : INT;
END_VAR

DevicesOnline := 0;
IF SW1__Online THEN DevicesOnline := DevicesOnline + 1; END_IF;
IF MIRA1__Online THEN DevicesOnline := DevicesOnline + 1; END_IF;

(* Ports: up = link; down = enabled but no link (Down is the driver's own
   derived member, AdminUp AND NOT OperUp — a port nobody enabled is
   neither). *)
up := 0; down := 0;
IF SW1__Online THEN
  IF SW1_Port01.OperUp THEN up := up + 1; END_IF; IF SW1_Port01.Down THEN down := down + 1; END_IF;
  IF SW1_Port02.OperUp THEN up := up + 1; END_IF; IF SW1_Port02.Down THEN down := down + 1; END_IF;
  IF SW1_Port03.OperUp THEN up := up + 1; END_IF; IF SW1_Port03.Down THEN down := down + 1; END_IF;
  IF SW1_Port04.OperUp THEN up := up + 1; END_IF; IF SW1_Port04.Down THEN down := down + 1; END_IF;
  IF SW1_Port05.OperUp THEN up := up + 1; END_IF; IF SW1_Port05.Down THEN down := down + 1; END_IF;
  IF SW1_Port06.OperUp THEN up := up + 1; END_IF; IF SW1_Port06.Down THEN down := down + 1; END_IF;
  IF SW1_Port07.OperUp THEN up := up + 1; END_IF; IF SW1_Port07.Down THEN down := down + 1; END_IF;
  IF SW1_Port08.OperUp THEN up := up + 1; END_IF; IF SW1_Port08.Down THEN down := down + 1; END_IF;
  IF SW1_Port09.OperUp THEN up := up + 1; END_IF; IF SW1_Port09.Down THEN down := down + 1; END_IF;
  IF SW1_Port10.OperUp THEN up := up + 1; END_IF; IF SW1_Port10.Down THEN down := down + 1; END_IF;
  IF SW1_Port11.OperUp THEN up := up + 1; END_IF; IF SW1_Port11.Down THEN down := down + 1; END_IF;
  IF SW1_Port12.OperUp THEN up := up + 1; END_IF; IF SW1_Port12.Down THEN down := down + 1; END_IF;
  IF SW1_Port13.OperUp THEN up := up + 1; END_IF; IF SW1_Port13.Down THEN down := down + 1; END_IF;
  IF SW1_Port14.OperUp THEN up := up + 1; END_IF; IF SW1_Port14.Down THEN down := down + 1; END_IF;
  IF SW1_Port15.OperUp THEN up := up + 1; END_IF; IF SW1_Port15.Down THEN down := down + 1; END_IF;
  IF SW1_Port16.OperUp THEN up := up + 1; END_IF; IF SW1_Port16.Down THEN down := down + 1; END_IF;
  IF SW1_Port17.OperUp THEN up := up + 1; END_IF; IF SW1_Port17.Down THEN down := down + 1; END_IF;
  IF SW1_Port18.OperUp THEN up := up + 1; END_IF; IF SW1_Port18.Down THEN down := down + 1; END_IF;
  IF SW1_Port19.OperUp THEN up := up + 1; END_IF; IF SW1_Port19.Down THEN down := down + 1; END_IF;
  IF SW1_Port20.OperUp THEN up := up + 1; END_IF; IF SW1_Port20.Down THEN down := down + 1; END_IF;
  IF SW1_Port21.OperUp THEN up := up + 1; END_IF; IF SW1_Port21.Down THEN down := down + 1; END_IF;
  IF SW1_Port22.OperUp THEN up := up + 1; END_IF; IF SW1_Port22.Down THEN down := down + 1; END_IF;
  IF SW1_Port23.OperUp THEN up := up + 1; END_IF; IF SW1_Port23.Down THEN down := down + 1; END_IF;
  IF SW1_Port24.OperUp THEN up := up + 1; END_IF; IF SW1_Port24.Down THEN down := down + 1; END_IF;
  IF SW1_Port25.OperUp THEN up := up + 1; END_IF; IF SW1_Port25.Down THEN down := down + 1; END_IF;
  IF SW1_Port26.OperUp THEN up := up + 1; END_IF; IF SW1_Port26.Down THEN down := down + 1; END_IF;
  IF SW1_Port27.OperUp THEN up := up + 1; END_IF; IF SW1_Port27.Down THEN down := down + 1; END_IF;
  IF SW1_Port28.OperUp THEN up := up + 1; END_IF; IF SW1_Port28.Down THEN down := down + 1; END_IF;
END_IF;
PortsUp := up;
PortsDown := down;

(* Rack health: any device faulted, or any device dark. *)
RackFault := (SW1__Online AND SW1.Fault) OR (MIRA1__Online AND MIRA1.Fault)
             OR NOT SW1__Online OR NOT MIRA1__Online;

(* The hottest reading in the rack, over the devices that report one. *)
RackMaxTempC := 0.0;
IF SW1__Online AND SW1.TempC > RackMaxTempC THEN RackMaxTempC := SW1.TempC; END_IF;
IF MIRA1__Online AND MIRA1.MaxTempC > RackMaxTempC THEN RackMaxTempC := MIRA1.MaxTempC; END_IF;
END_PROGRAM
