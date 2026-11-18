# bin-fill — a sequence tested against virtual devices

A small bin-fill sequence and the three field devices it talks to, run as
**virtual devices**: simulated devices that answer the project's own Modbus
and OPC UA bindings as the real ones would, so a sequence test exercises the
register map, node ids and scaling, not only the logic.

- **CONV**: the conveyor, on an ABB ACS355 drive (FMBT-21, ABB Drives
  profile over Modbus TCP).
- **RB1**: a 16-in/16-out relay board (Modbus TCP). Relay 1 opens the gate;
  input 1 is the gate's open switch.
- **IOLM**: a Pepperl+Fuchs ICE3 IO-Link master (OPC UA). Port 1 is the
  bin's high-level switch, port 2 a plug switch on the conveyor head.

The sequence (`fill.st`): start the drive (0x0476, then 0x047F once it
reports READY TO SWITCH ON), open the gate, fill until the high level,
close the gate, purge the conveyor for 3 s, stop. A plug or a drive trip
stops everything.

```sh
naut check .            # validate offline
naut test  .            # the sequence against the virtual devices, virtual time
naut devices up .       # the three devices on real sockets, until ctrl-c
```

## Files

| File | What it is |
|---|---|
| `nautilus.yaml`, `fill.st` | the controller: tags, the sequence, a Modbus and an OPC UA client driver |
| `modbus_manifest.yaml`, `opcua_manifest.yaml` | the controller's bindings, as for real devices |
| `virtual-devices.yaml` | the three devices: a built-in profile each, at the addresses the manifests name |
| `devices/plant.st` | the process: the gate opens after 1 s, the bin fills at 5 %/s while the gate is open and the conveyor runs, the high-level switch makes at 90 % |
| `fill_test.yaml` | four sequence tests: fill and purge, motor load, a plug, a drive trip |

`naut devices profiles` lists the built-in profiles; `naut devices show
<profile>` prints a profile's registers or node ids and the document each
row came from.

## The tests

`devices: true` in `fill_test.yaml` runs the suite against the devices in
`virtual-devices.yaml`. The controller's own drivers decode what the devices
serve, in process and in virtual time, so the run is deterministic and needs
no sockets. Two step keys matter here:

- the controller's inputs come from the devices: `given:` on one of them is
  an error, because the device owns it;
- `plant:` writes the plant's side: a device point, a simulator hook or a
  model variable. The plug test sets `IOLM_Port2_SioInput`; the trip test
  sets `CONV_Trip`.

A mistake in the bindings fails a test: a status word at the wrong register
(the drive answers exception 02), a mistyped node id, a current scaled by
0.01 instead of 0.1, a wrong unit id. So does a start sequence that relies
on its first scan's control word reaching the drive (the Modbus driver
records the first output push as its baseline and does not send it). The Go
tests in `cmd/naut/devices_test.go` make each of these edits to a copy of
this folder and check that the suite fails.

## On real sockets

`naut devices up .` starts the devices on loopback, each on its own port,
and prints which address each one stands in for:

```
DEVICE  PROFILE             LISTEN                     UNIT  STANDS IN FOR
CONV    abb-acs355-fmbt21   127.0.0.1:45527            1     10.77.1.101:502
IOLM    pepperl-fuchs-ice3  opc.tcp://127.0.0.1:46385  0     opc.tcp://10.77.1.103:4840
RB1     asi-275m            127.0.0.1:35211            1     10.77.1.102:502
```

Any Modbus or OPC UA client can connect to them. To bind a device to a fixed
address, for example in a container or on another machine, give it
`listen:` in `virtual-devices.yaml` (`listen: 0.0.0.0:502`). `naut run .`
does not yet redirect the project's drivers to the virtual devices; the
drivers dial the addresses in the manifests.

The addresses (10.77.1.x) are made up. The design is in
[docs/design/virtual-devices.md](../../docs/design/virtual-devices.md).
