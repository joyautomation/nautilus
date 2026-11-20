---
title: Read one Modbus register, with no hardware
description: "Describe a device from its datasheet, generate the bindings, start a simulated device on your computer, and watch the controller read its registers."
sidebar:
  order: 1
---

Modbus TCP is the protocol most field devices speak. In this lesson a
controller reads a tank level transmitter, and you need no hardware: a
simulated device runs on your computer. It takes about ten minutes. You need
`naut` ([install](/learn/getting-started/install/)) and a project
([your first controller](/learn/getting-started/first-controller/)).

## The idea

A Modbus device is a numbered list of **registers**. The datasheet says what
is at each number, how to read it (a 16-bit integer, a 32-bit float), and
how to scale it (the level is sent as tenths of a percent). nautilus keeps
that knowledge in one file you write by hand, the **device map**, and
generates everything else from it. You never edit the generated files.

## 1. Start a project and write the device map

```sh
naut new tank --template minimal
cd tank
```

Create `devices.yaml` in the project folder. This says: a *type* of device (what the datasheet
tells you), and an *instance* of it (where this one is):

```yaml
devices:
  level-transmitter:
    description: Tank level transmitter
    registers:
      - {tag: Level, address: 0, format: int16, scale: 0.1, unit: "%", desc: tank level}
      - {tag: Flow,  address: 1, format: float32, unit: "m3/h", desc: inlet flow}
instances:
  - {id: LT1, type: level-transmitter, host: 127.0.0.1, port: 15020, unit-id: 1, desc: the bench simulator}
```

`address` is the register number from the datasheet counted from 0. The
`format` and `scale` turn the raw register into the number you want: the
raw `425` in register 0 is `42.5` after `scale: 0.1`. `unit-id` picks which
device answers when several share one address (a gateway). `host` and `port`
are where it lives; 502 is the Modbus default, and here we use a free port
on your own computer.

## 2. Generate the bindings

```sh
naut modbus import --map devices.yaml
```

```text
wrote modbus_manifest.yaml (1 sources, 2 tag bindings)
wrote tags/modbus.yaml — compose it with `tag-files: [tags/modbus.yaml]`
```

Two files appeared, and both start with a comment saying not to edit them:
`modbus_manifest.yaml` is how to poll the device, and `tags/modbus.yaml`
declares the tags. The tags are named with the instance id in front,
`LT1_Level` and `LT1_Flow`, so two devices of one type do not clash. Use
those names in your program. Open `tags/modbus.yaml` and there are two more:

```yaml
- { name: LT1__Online, role: input, type: BOOL, desc: "source LT1 answering — interlock on this" }
- { name: LT1__Outages, role: input, type: DINT, desc: "times LT1__Online has fallen since the driver started" }
```

You did not ask for these. For every device the controller adds an
`__Online` tag, true while the device is answering, and an `__Outages`
counter of how many times it has stopped. Step 3 uses the first.

## 3. Point the project at the device

Replace `nautilus.yaml` with:

```yaml
name: tank

server:
  addr: "localhost:8080"

tasks:
  - program: program.st
    scan: 100ms

tag-files: [tags/modbus.yaml]

tags:
  - { name: HighLevel, role: output, type: BOOL, init: false, desc: Level above 80 % }
  - { name: DeviceUp,  role: output, type: BOOL, init: false, desc: The transmitter is answering }

driver:
  type: modbus
  manifest: modbus_manifest.yaml
  scan-rate: 1s
```

`driver: modbus` replaces the `memory` loopback, and `tag-files` brings in
the generated tags. `HighLevel` and `DeviceUp` are `output` tags: tags the program writes.
Replace `program.st` with two lines of logic that use device tags, and delete
the scaffold's `tank_test.yaml` (it tests the old program):

```st
PROGRAM Tank
HighLevel := LT1__Online AND LT1_Level > 80.0;
DeviceUp  := LT1__Online;
END_PROGRAM
```

`HighLevel` only turns on while the device is answering: a level that is stale
because the transmitter is down should not raise an alarm, and a program should
say so.

```sh
naut check
```

`naut check` builds the read plan from the manifest without any device
running, and should say `0 with errors`.

## 4. Start the simulated device

A bench device ships inside `naut`. It answers for the manifest you
generated, with the values you give it. In `seed.json`, in the project folder, give it values in
real units (the scaling is undone for you):

```json
{"LT1_Level": 42.5, "LT1_Flow": 3.25}
```

In a second terminal, in the project folder:

```sh
naut modbus serve --manifest modbus_manifest.yaml --values seed.json --listen 127.0.0.1:15020
```

Check what is on the wire with the commissioning tool, which reads a range
of registers and shows each one in every format at once:

```sh
naut modbus browse --host 127.0.0.1 --port 15020 --unit 1 --from 0 --count 3
```

```text
addr  raw     uint16  int16  int32       uint32      float32      float64
0     0x01A9  425     425    27869264    27869264    6.21731e-38
1     0x4050  16464   16464  1078984704  1078984704  3.25
2     0x0000  0       0
```

Register 0 is `425` as an `int16` and register 1 is `3.25` as a `float32`:
the formats in the device map. A 32-bit value spans two registers, so the
`float32` in row 1 is only shown once register 2 is read too: that is why the
command asks for three. On a real device this table is how you find
out which format a register really is.

## 5. Run the controller against it

```sh
naut run
```

You should see `modbus: connected` and the dashboard on
<http://localhost:8080> (if that port is already in use, `naut run` says so and
prints the address it moved to; use that one). The tags now come from the
device:

```sh
curl -s localhost:8080/api/state | jq -c .tags
```

(`jq` only tidies the output; without it, plain `curl` prints the whole state.)

```text
{"DeviceUp":true,"HighLevel":false,"LT1_Flow":3.25,"LT1_Level":42.5,"LT1__Online":true,"LT1__Outages":0}
```

`HighLevel` is false because 42.5 is below 80, and `DeviceUp` is true because
the device is answering.

## Is the device there?

Stop the simulator (Ctrl+C in its terminal) and read the tags again after a few
seconds:

```text
{"DeviceUp":false,"HighLevel":false,"LT1_Flow":3.25,"LT1_Level":42.5,"LT1__Online":false,"LT1__Outages":1}
```

`LT1__Online` has gone false and `LT1__Outages` has counted the outage. The
level and flow still show their last values, so the logic must not trust them
on their own: that is why `HighLevel` asked for `LT1__Online` first. The
dashboard's driver card shows an error, and `/api/state` gains a `quality`
entry for the two readings, `LT1_Level` and `LT1_Flow`, as `stale`. A device that never answers at all
reports `notConnected`.

Start the simulator again. The controller reconnects by itself, with no
restart, but not at once: after a failed attempt the driver waits 1 second, then
2, 4 and 8 before trying again, so read the tags again after up to 20 seconds.
The controller's log shows each try.

```text
{"DeviceUp":true,"HighLevel":false,"LT1_Flow":3.25,"LT1_Level":42.5,"LT1__Online":true,"LT1__Outages":1}
```

`LT1__Online` is true again; `LT1__Outages` stays at 1, a count of the times it
fell since the driver started. (The `quality` entries go away: a good tag
carries none.)

## Next

To use a real device, change `host:` (and `port:`, usually 502) in the
instance and run the import again; nothing else changes. The [Modbus
guide](/guides/modbus/) covers word and byte order, writes, and keep-alive
rewrites.
