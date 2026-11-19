---
title: IO-Link devices on a master (preview)
description: Sensors on an IO-Link master's ports as typed tags decoded from their IODD, with an identity check that keeps a wrong device from receiving outputs, and their parameters read and written through the controller's API.
---

:::caution[Preview]
Run against virtual masters (the Pepperl+Fuchs, ifm and SICK address spaces
built from their manuals), not yet against a real master. The node ids for the
ifm and SICK masters are marked `unverified` in their profiles until someone
reads them off a real device. Try it against your master before you depend on
it.
:::

An IO-Link master is an ordinary OPC UA device (see
[Reading OPC UA](/guides/opcua-client/)); the sensor on each of its ports is
described by an **IODD**, an XML file its maker ships. nautilus reads the IODD
so that nobody writes a decode by hand, checks that the right sensor is on the
port, and reaches the sensor's parameters.

## A port device

Import the IODD into the project, then name it on the master's row:

```
naut iolink import acme-level100-20261118-IODD1.1.xml
```

```yaml
devices:
  - name: M
    profile: sick-sig300
    endpoint: 10.0.0.30
    ports:
      1: { name: LEVEL1, iodd: acme-level100-20261118-IODD1.1.xml }
```

`LEVEL1` is a struct tag: one member per process data item, typed by the IODD
(a scaled integer is a `REAL`), with `PdValid` when the master says whether its
image is valid. The IODD copy in `iodd/` is the pin: an upgrade of nautilus
never changes how a deployed controller decodes a port. Two more tags come with
it, `LEVEL1__Identity` and `LEVEL1__IdentityFault`, and, for a sensor with
process data out, a command tag per item (`LEVEL1_Mode`) and `LEVEL1__Rearm`.

## Identity, and what a wrong sensor does to outputs

The master reports each device's vendor and device id. When they are not the
IODD's, `LEVEL1__Identity` says `WrongDevice` or `NoDevice` and the sensor's
bytes are **not** decoded. `LEVEL1__IdentityFault` is true and, when the project
has `alarms:`, a high-priority alarm is raised.

A master keeps the last process data out it was given and sends it to whatever
is on the port, so **not writing is not safe**. A port with process data out is
therefore *armed*:

- the first time its identity is Ok, at a power-up or a takeover, it is armed
  with no action, and sends your program's current values;
- when the device stops being the right one while running (a swap), nothing is
  sent, and the master's own "PDO valid" flag is driven false where it has one,
  so the device falls back to its fail-safe values;
- when the right device is back the port stays quiet, reads `AwaitingRearm`, and
  keeps the alarm active, until `LEVEL1__Rearm` is pulsed (false, then true).
  The pulse sends the values your program holds *then*, never the ones from
  before the swap, and is refused while the device is not the right one.

A master whose profile has no "valid" flag cannot drop it: a wrong device on
its port is sent the master's last process data out until you stop it, and
`naut devices show` and `naut check` say so for that master.

## Parameters

A sensor's parameters (a setpoint, a tag, a window) are read and written through
the master by ISDU. They go through the running controller's API, never to the
master directly, so a write is authorized and journalled:

```
naut iolink param list  --host plc1 LEVEL1
naut iolink param get   --host plc1 LEVEL1 V_Setpoint
naut iolink param set   --host plc1 LEVEL1 V_Setpoint 500
```

- A **read** works whichever device is on the port and says which; one taken
  from a device that is not the one expected says so.
- A **write** needs the controller's write credential (`NAUTILUS_TOKEN`), is
  journalled as `param.write <port device>/<variable>` with the value held
  before and after, and is **refused** unless the device on the port is the
  expected one, so a parameter never reaches the wrong sensor.
- A value is checked against the IODD (access rights, range, type) before
  anything is sent.
- If the device on the port changes *while* a write is under way, or the
  write was sent and the master did not answer (a timeout, a lost
  connection), the answer is **outcome uncertain** (502, and the journal
  records it as unknown), not success and not failure: the device may have
  taken it. `naut iolink param set` says so, does not retry, and tells you to
  check with `get`. The identity check is as fresh as the master's last scan
  of its ids, so the window is narrowed, not closed.
- One ISDU call at a time per port: a second request (read or write) while one
  is in flight is refused with `429` and `Retry-After`, with nothing sent to
  the master, so parameter reads cannot flood it. Reads will sit behind the
  read gate of the auth work (#708) when that lands.
- Over a master that carries a parameter as hex text (the ifm masters), a
  parameter over 32 octets cannot be read or written.

### With a redundant pair

A parameter lives on the sensor, so a new leader has nothing to replay: a
parameter write is **leader-only and has no entry in the operator-intent log**.
The audit journal is its record. A standby refuses a write (`503`), as it does
for other writes; reads are answered by whichever controller holds the master's
session.
