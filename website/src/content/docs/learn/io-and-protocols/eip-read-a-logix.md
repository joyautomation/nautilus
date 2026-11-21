---
title: Read a Logix controller over EtherNet/IP
description: "Browse an Allen-Bradley Logix controller, generate the bindings, read a level and a motor struct, and command a pump back, with a simulated controller if you have none."
sidebar:
  order: 6
---

Most plants that have Allen-Bradley PLCs have a ControlLogix or CompactLogix
somewhere, and the usual first job for a new controller is to read it. In this
lesson your controller talks EtherNet/IP to a Logix controller: you browse its
tags, generate the bindings, read a level and a motor, and command a pump back.
About fifteen minutes. You need `naut`
([install](/learn/getting-started/install/)). Make a project for it and stay in
its folder for the rest of the lesson:

```sh
naut new tankmon --template minimal
cd tankmon
```

## A controller to practise on

If you have no Logix controller to hand, `naut` can play one. Save this as
`surface.json` in a folder of its own outside the project (the simulated
controller is not part of your controller). It describes four things a Logix
controller would offer: a tank level, a pump command, and a motor with a speed
and a running bit, which is a **UDT**, a structure you define on the controller:

```json
{
  "controllerName": "Plant1",
  "templates": [
    {"name": "Motor", "members": [
      {"name": "Speed", "datatype": "REAL"},
      {"name": "Running", "datatype": "BOOL"}
    ]}
  ],
  "symbols": [
    {"name": "Tank_Level", "datatype": "REAL"},
    {"name": "Pump_Run", "datatype": "BOOL"},
    {"name": "Pump1", "datatype": "Motor"}
  ],
  "tags": [
    {"path": "Tank_Level", "datatype": "REAL"},
    {"path": "Pump_Run", "datatype": "BOOL"},
    {"path": "Pump1.Speed", "datatype": "REAL"},
    {"path": "Pump1.Running", "datatype": "BOOL"}
  ]
}
```

Start values for the level and the motor speed go in `values.json`, beside it:

```json
{"Tank_Level": 50.0, "Pump1.Speed": 1450.0}
```

Start the simulated controller from that folder and leave it running:

```sh
naut logix emulate --surface surface.json --values values.json --ramp
```

```text
emulating ControlLogix "Plant1" on 127.0.0.1:44818 — 3 tags, 0 programs, 1 template, 4 leaves
ctrl-c to stop
```

`--ramp` makes the numbers drift, so you can see them move. It listens on
port 44818, the port every Logix controller uses, so on a real controller you
would just give its IP address where this lesson says `127.0.0.1`. (If the
address is already in use, another controller or emulator is running on this
computer; stop it first.)

## 1. Look at what it offers

In a second terminal, in the project folder:

```sh
naut eip browse --host 127.0.0.1
```

```text
Tank_Level                                         REAL
Pump_Run                                           BOOL
Pump1                                              Motor
```

`browse` only reads: it never writes. Each line is a tag on the controller and
its type. `Pump1` is a `Motor`, the UDT. A real controller lists hundreds of
tags; the `--tags` option on the next command picks the ones you want.

## 2. Generate the bindings

Import all of it, and say which tag the controller may write:

```sh
naut eip import --host 127.0.0.1 --format yaml --writable 'Pump_Run'
```

```text
browsing 127.0.0.1 (slot 0)...
found 3 tags, 1 templates, 0 programs
wrote eip_types.st (1 types) and eip_manifest.yaml (3 tag bindings)
wrote tags/eip.yaml — compose it with `tag-files: [tags/eip.yaml]`
online tag: eip__Online (no eip driver in nautilus.yaml; the default driver name)
```

Three generated files, never edited by hand: re-run the import when the
controller's program changes. `eip_types.st` turns the `Motor` UDT into a
structure your program can use, `eip_manifest.yaml` is how to read each tag,
and `tags/eip.yaml` declares `Pump1`, `Pump_Run`, `Tank_Level` and one more,
`eip__Online`: true while your controller is talking to the PLC. Writing is
opt-in per tag: `writable: true` is only on `Pump_Run`.

## 3. Point the project at the controller

In `nautilus.yaml`, replace the scaffold's `tags:` entries (the sample `Sensor`,
`Setpoint` and `Actuator`) with one tag for the operator, and replace the
`driver:` section:

```yaml
tags:
  - { name: PumpStart, role: setpoint, type: BOOL, init: false, desc: Operator start for the pump }

tag-files: [tags/eip.yaml]

driver:
  type: eip
  host: 127.0.0.1
  manifest: eip_manifest.yaml
  scan-rate: 1s
```

`PumpStart` is a `setpoint`: a value an operator can write, `false` until they
do. (`tag-files` brings in the tags the import generated; `scan-rate` is how
often the controller is polled.) Replace `program.st`, and delete the scaffold's
test file `tankmon_test.yaml` (it tests the old program):

```st
PROGRAM Tank
VAR_EXTERNAL
    Tank_Level  : REAL;
    Pump_Run    : BOOL;
    eip__Online : BOOL;
END_VAR
Pump_Run := PumpStart AND eip__Online AND Tank_Level > 45.0;
END_PROGRAM
```

The pump runs only when an operator has asked for it, the controller is
answering, and the level is above 45. `PumpStart` needs no declaration because
tags from `nautilus.yaml` are in scope; the `VAR_EXTERNAL` block names only the
imported ones the program uses (`Pump1` is not used here, so it is left out).
`naut check` says `0 with errors`, with no
controller needed: it checks the manifest offline.

## 4. Run it, and check from the other side

```sh
naut run
```

The dashboard is at <http://localhost:8080> (if that port is already in use,
`naut run` says so and prints the address it moved to; use that one in the
commands below).

```sh
curl -s localhost:8080/api/state | jq -c .tags
```

(`jq` only tidies the output; without it, plain `curl` prints the whole state.)

```text
{"Pump1":{"Running":false,"Speed":1452.7},"PumpStart":false,"Pump_Run":false,"Tank_Level":50.4,"eip__Online":true}
```

The numbers move, so yours will differ, and the real output prints them with many more decimals (shortened here). Look at `Pump1`: the whole UDT arrives
as one value, with its members inside. The controller is reading the PLC, and
`Pump_Run` is false because nobody has asked for the pump. Ask for it:

```sh
curl -s -X POST localhost:8080/api/tags -d '{"name":"PumpStart","value":true}'
curl -s localhost:8080/api/state | jq -c .tags
```

```text
{"Pump1":{"Running":false,"Speed":1494.9},"PumpStart":true,"Pump_Run":true,"Tank_Level":59.0,"eip__Online":true}
```

That is what your controller says. Ask the controller too, with a Logix client
that is not nautilus, so you know the command really arrived. This one is
[pycomm3](https://github.com/ottowayi/pycomm3), a popular Python library for
Logix controllers (`pip install pycomm3`). Save it as `ask.py` beside
`surface.json` and run it with the Python you installed pycomm3 into:

```python
from pycomm3 import LogixDriver

with LogixDriver("127.0.0.1") as plc:
    level = plc.read("Tank_Level")
    pump = plc.read("Pump_Run")
    print("server side: Tank_Level =", round(level.value, 1), " Pump_Run =", pump.value)
```

```text
server side: Tank_Level = 50.8  Pump_Run = True
```

`Pump_Run` on the controller is `True`: your controller wrote it. (Before you
asked for the pump, the same script printed `Pump_Run = False`.)

## 5. When the controller goes away

Stop the simulated controller (Ctrl-C in its terminal), and read the tags again
after a few seconds:

```text
{"Pump1":{"Running":false,"Speed":1507.2},"PumpStart":true,"Pump_Run":false,"Tank_Level":55.1,"eip__Online":false}
```

`eip__Online` is false, and `Pump_Run` followed it down because the program
asked for it. The level and the motor keep their last values (so a program that
ignores `__Online` would act on stale numbers), and the dashboard's driver card
says `error`, `Connect failed — retrying`. Start the simulated controller again,
with the same command: your controller reconnects by itself, with no restart,
within about twenty seconds. It then sends the pump command again, because the
restarted controller has `Pump_Run` back at `False`:

```text
{"Pump1":{"Running":false,"Speed":1450.8},"PumpStart":true,"Pump_Run":true,"Tank_Level":51.9,"eip__Online":true}
```

## 6. Test it without any controller

Everything above needed a controller, real or simulated. The logic does not:
`naut test` never opens a socket, and it lets the test play the controller's
part. Stop `naut run` and the simulated controller, and put this in `tankmon_test.yaml`:

```yaml
tests:
  - name: pump runs only with the controller answering and the level up
    steps:
      - given: { PumpStart: true, eip__Online: true, Tank_Level: 50.0 }
        scans: 1
        expect: { Pump_Run: true }
      - given: { Tank_Level: 40.0 }
        scans: 1
        expect: { Pump_Run: false }
      - given: { Tank_Level: 50.0, eip__Online: false }
        scans: 1
        expect: { Pump_Run: false }
```

```sh
naut test .
```

```text
tankmon_test.yaml
  ok    pump runs only with the controller answering and the level up

1 tests, 1 passed, 0 failed
```

The `given:` lines write what a poll would have written, including
`eip__Online`, so the same test keeps passing when the real controller is
connected. It is how you would test the last step above (the controller going
away) in the pipeline, with nobody pulling a cable.

## Before a real controller

Check three things before you point this at a plant controller.

- **Writing.** The driver commands every output tag as soon as it starts, with
  the value your program gives it, because it has no memory of what was there
  before. Give each writable tag an `init:` that is safe to command, and mark
  only the tags your program must command as `--writable`.
- **Where it connects.** The controller's IP address goes on `host:`, and `slot:`
  is the processor's backplane slot (0 unless it sits in a chassis with other
  modules ahead of it).
- **What it cannot do.** The driver reads and writes tags on a Logix controller.
  A drive, a valve or an I/O block with no tag list is not reachable this way,
  and neither is cyclic I/O.

The [EtherNet/IP guide](/guides/ethernet-ip/) covers scan classes (some tags
faster, some slower), how UDTs with access-restricted members are read, and
exactly what is not supported.

## Next

[Coming from Logix](/learn/logix/read-a-logix-project/) is the other direction:
reading the PLC's own project file as text instead of reading its tags live.
