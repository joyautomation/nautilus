# plclab — a nautilus controller for MHJ PLC-Lab

[PLC-Lab](https://www.mhj-wiki.de/en/plc-lab/) is a 2D plant simulator used
to teach automation: it draws the plant (tanks, conveyors, switches, lamps)
and a PLC runs it. It drives a CODESYS controller over OPC UA, through a
container named `PlcLabConnector`. This project is that controller, in
nautilus: PLC-Lab connects straight to its OPC UA server, with no bridge
script and no certificates.

The plant here is a small tank: it fills to the high level switch, holds
full for three seconds, drains to the low switch and fills again, started
and stopped from two pushbuttons.

```sh
naut check .    # validate offline
naut test  .    # the acceptance suite, virtual time, no PLC-Lab needed
naut run   .    # OPC UA on opc.tcp://localhost:4840, dashboard on http://localhost:8080
```

`naut check` prints one warning, on purpose: `PlcLabConnector.Inputs` is an
array, so a client may write its bytes one at a time but not the whole array
in one write. One at a time is what PLC-Lab does.

## Point PLC-Lab at it

In PLC-Lab, add an **OPC UA** device (PLC-Lab 1.6 or later) and set:

| Setting | Value |
|---|---|
| IP address | the machine running `naut run`: `127.0.0.1` on the same PC |
| Port | `4840` |
| Container name | `PlcLabConnector` (fixed) |
| Address mode | **Address mode for CodeSys systems** |
| User / password | empty: anonymous login |

The endpoint is `opc.tcp://<host>:4840`, security policy `None`. The
manifest listens on `localhost`, which is right when PLC-Lab and nautilus run
on the same PC. For PLC-Lab on another machine, change `opcua.endpoint` in
`nautilus.yaml` to `opc.tcp://0.0.0.0:4840`, on a lab network only:
anonymous writes mean anyone who can reach the port may write what
`writable` opens, and `naut run` says so with an error line at every start.

On CODESYS you would also add the PlcLabSync function, the GVL and the
symbol configuration. Here they are already in the project:

| CODESYS | here |
|---|---|
| DUT `PlcLabData` | `PlcLabArea` in [`types.st`](types.st) |
| GVL `PlcLabConnector` | the struct tag `PlcLabConnector`, of type `PlcLabConnectorData` ([`types.st`](types.st), [`nautilus.yaml`](nautilus.yaml)) |
| Symbol configuration | `opcua:` in [`nautilus.yaml`](nautilus.yaml): `writable` opens `Inputs`, `AreaInputs` and `AreaOutputs`, nothing else |
| `PlcLabSync(Inputs:=TRUE, Outputs:=TRUE)` first in `PLC_PRG` | [`plclab.st`](plclab.st): bytes in, logic, bytes out, in one scan |
| `PLC_PRG`'s own logic | [`lib/tank.ld`](lib/tank.ld), a ladder function block |

## I/O map

In PLC-Lab's symbol table, give the plant's objects these addresses. Every
signal is in byte 0, so PLC-Lab's area tables are one input byte and one
output byte.

| Address | Tag | Meaning |
|---|---|---|
| I0.0 | `StartPB` | start pushbutton, normally open |
| I0.1 | `StopPB` | stop pushbutton, **normally closed**: TRUE while not pressed |
| I0.2 | `LevelLow` | low level switch, TRUE while the level is above it |
| I0.3 | `LevelHigh` | high level switch, TRUE while the level is above it |
| Q0.0 | `FillValve` | fill valve |
| Q0.1 | `DrainValve` | drain valve |
| Q0.2 | `RunLamp` | running lamp |
| Q0.3 | `FullLamp` | tank full lamp |

The process image is `IB0`–`IB15` and `QB0`–`QB15` (`ib` and `qb` in
`plclab.st`). Map more signals by giving them bits in those bytes and names
in `plclab.st`; a byte PLC-Lab maps outside them is skipped. `PlcLabMapped`
turns TRUE once PLC-Lab has written its area tables.

Because the stop button is normally closed, the tank does nothing until
PLC-Lab is connected: with no connection, `StopPB` reads FALSE, as a broken
wire would.

## How the connector works

PLC-Lab writes `Inputs` (its sensors, as bytes) and two area tables,
`AreaInputs` and `AreaOutputs`, ten entries of `CntBytes`, `Addr` and
`IoOfs` each. Entry *a* says: `CntBytes` bytes, starting at `Inputs[Addr]`
(or `Outputs[Addr]`), are process image bytes starting at `IoOfs`. The first
entry with `CntBytes = 0` ends the table. PLC-Lab reads `Outputs`, and the
four `SIZE_` words tell it how long the arrays are.

Every scan, `plclab.st` copies the mapped input bytes into `ib`, puts their
bits on named tags, runs the tank, packs the named outputs into `qb`, and
copies the mapped bytes of `qb` into `Outputs`. It writes an output byte
only when it changed. The connector is one tag, and a scan that writes any
part of it commits all of it, so a scan that wrote every scan could put
back an older copy of an input byte PLC-Lab wrote while the scan ran. Now
that can only happen when an output byte changes in the same scan as a
PLC-Lab write, which is rare, and the next write of that byte puts it right.

Over OPC UA, PLC-Lab reads and writes single bytes by string node id,
`ns=1;s=PlcLabConnector.Inputs[17]`, and the area fields as
`ns=1;s=PlcLabConnector.AreaInputs[0].CntBytes` (UInt16). The project
folder is `Objects/plclab/PlcLabConnector`, in namespace 1
(`urn:joyautomation:nautilus:plclab`).

One difference from MHJ's PlcLabSync: its copy loop runs from 0 to
`CntBytes` inclusive, one byte more than the area holds. This project copies
exactly `CntBytes` bytes, as the bridge PLC-Lab is known to work with does.

## What is verified, and what is not yet

Verified, in CI and on a laptop:

- `naut test .` covers the copy logic and the tank in virtual time
  ([`plclab_test.yaml`](plclab_test.yaml)).
- `TestForeignPLCLabShapedClient` (`opcua/server/foreign_plclab_test.go`)
  runs this project as `naut run` would and drives it from asyncua the way
  PLC-Lab drives a CODESYS controller, as far as MHJ's manual and a
  third-party server PLC-Lab is known to accept show: anonymous on `None`,
  the container found by browse name, the area tables written as UInt16,
  input bytes written one at a time by string node id, `Outputs` polled
  whole. The tank answers every step.

Not yet verified against PLC-Lab itself:

- **The namespace.** nautilus serves the connector in namespace 1. If
  PLC-Lab expects a fixed namespace index or URI, or a CODESYS-style node id
  (`|var|<device>.Application.PlcLabConnector...`), it will not find it.
- **Single bytes, not ranges.** If PLC-Lab writes `Inputs` with an
  `IndexRange`, or whole, nautilus refuses that today
  (`Bad_WriteNotSupported`).
- **Access levels.** The `Inputs` array node itself reads `CurrentRead`; its
  elements read `CurrentRead|CurrentWrite`. A client that checks the
  array's `AccessLevel` before writing elements would stop there.

If you run it against PLC-Lab, please report what happened, with `naut
run`'s console output (every refused write is logged with its node id and
reason), at https://github.com/joyautomation/nautilus/issues.
