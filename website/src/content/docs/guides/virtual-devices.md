---
title: Virtual devices
description: Stand up simulated drives, relay boards and IO-Link masters that answer the project's own Modbus and OPC UA bindings, so a sequence is tested against the register map and node ids and not only the logic.
---

A **virtual device** stands in for one field device and speaks its
protocol: its Modbus register map, or the address space of its OPC UA
server. Each one is built from the device's public documentation, and
`naut devices show <profile>` says which document each row came from. A
project lists its devices, and the same list serves two jobs:

- **`naut test`** runs a sequence against them in process, on the virtual
  clock, with no sockets.
- **`naut devices up`** serves them on real sockets, so a controller (this
  one, or any other) can be commissioned against them before the plant
  exists.

## Declare the devices

A project that already names its field devices in a `devices:` list in
`nautilus.yaml` gets one virtual device per row. A project that does not
lists them in `virtual-devices.yaml`, next to the manifest:

```yaml
devices:
  - { name: CONV, profile: abb-acs355-fmbt21,  endpoint: 10.77.1.101, init: { RampS: 2.0 } }
  - { name: RB1,  profile: asi-275m,           endpoint: 10.77.1.102 }
  - { name: IOLM, profile: pepperl-fuchs-ice3, endpoint: 10.77.1.103 }
model:
  program: devices/plant.st    # the process: an ST PROGRAM that moves the devices
  scan: 100ms
```

`endpoint` is the address the project's own driver dials, so the
controller's bindings stay as they are for the real device. `naut devices
profiles` lists the built-in profiles (drives, relay boards, a burner's
temperature controller, and the IO-Link masters of three makers).
`naut devices add <profile>` copies a profile into the project's
`profiles/` folder, so the project pins its own copy.

## Test a sequence against them

A test file with `devices: true` runs against the virtual devices instead
of the bare memory driver. The project's drivers are answered in process
through each device's registers or node ids, and the devices' simulators
and the process model step on the same virtual clock:

```yaml
devices: true
tests:
  - name: fills the bin and purges the conveyor
    steps:
      - given: { StartReq: true }
        until: 10s
        expect: { Step: 2, GateOpen: true }
```

Two rules differ from an ordinary test. A `given` on an input that a
device delivers is an error, because the device delivers it. A step's
`plant:` writes the process instead. A request the device refuses (an
exception code, a node it does not serve) fails the test.

## Serve them on real sockets

```sh
naut devices check .   # load the profiles, simulators and model; opens no sockets
naut devices up .      # serve every device on its wire face until ctrl-c
```

`naut devices render` prints what a project's `devices:` rows derive at
load: the struct types, the tags and the driver bindings.

## What to know

- `naut run` does not start the virtual devices. Use `naut devices up` in a
  second terminal.
- The virtual devices are test fixtures. `naut deploy` leaves
  `virtual-devices.yaml` and the files it names off the controller.
- A virtual device is only as true as its documentation. The IO-Link
  masters' node ids for two makers are marked `unverified` until read off a
  real master (see [IO-Link](/guides/io-link/)).

The project to copy is `examples/bin-fill` in the repository: a conveyor
drive, a relay board and an IO-Link master, a four-test sequence suite, and
`naut devices up .`. See also [Testing](/reference/testing/) and
[Modbus](/guides/modbus/).
