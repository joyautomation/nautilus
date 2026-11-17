---
title: PROFINET IO device (experimental)
description: nautilus as a PROFINET IO device to a PLC. The layout and tags live in one manifest, the GSDML the PLC imports is generated from it, and nothing on the host network changes behind your back.
---

:::caution[Experimental]
The device side is built and tested against an in-process controller in
CI. It has **not yet run against a real PLC**, so the commands and file
shapes may change. RT_CLASS_1 only, one AR, no alarms raised. Nothing
here makes nautilus a safety device: keep safety in the hardwired chain.
:::

With `type: profinet-device`, nautilus appears on a PROFINET network as
an IO device. A PLC (an S7-1500 in TIA Portal, for example) configures it
from a GSDML file like any other station, connects, and exchanges cyclic
data with it. Data flows both ways:
- The PLC's **outputs** arrive as nautilus **input** tags.
- nautilus **output** tags are the PLC's **inputs**.

## The manifest

```yaml
# profinet_device.yaml
interface: eth1            # the NIC on the PROFINET network
station: nautilus-1        # NameOfStation the PLC looks for
vendor-id: 0x....          # what the GSDML and DCP say this device is
device-id: 0x....
min-cycle: 8ms             # the shortest cycle offered (MinDeviceInterval)
slots:
  - slot: 1
    module-ident: 0x00000101
    subslots:
      - {subslot: 1, submodule-ident: 0x00000001, inputs: 8, outputs: 4}
tags:
  - {name: Bin1_Level, slot: 1, subslot: 1, dir: input,  offset: 0, format: float32}
  - {name: Bin1_Fill,  slot: 1, subslot: 1, dir: output, offset: 0, bit: 0, format: bool}
```

**Directions are the PLC's**, because that is how the GSDML and the
engineering tool show them:
- `inputs:` and `dir: input` are data nautilus provides;
- `outputs:` and `dir: output` are data the PLC commands.

**Formats** are `bool` (with `bit: 0–7`), `int8`, `uint8`, `int16`,
`uint16`, `int32`, `uint32`, `int64`, `uint64`, `float32` and `float64`,
all big-endian as PROFINET is. `naut check` rejects a tag that overlaps
another or does not fit its subslot. The limits are 24 subslots and 244
bytes each way.

```yaml
# nautilus.yaml
driver:
  type: profinet-device
  manifest: profinet_device.yaml
tag-files: [tags/profinet.yaml]
```

```sh
naut profinet tags profinet_device.yaml     # tags/profinet.yaml, IEC types, the PLC's outputs as inputs
naut profinet gsdml profinet_device.yaml    # GSDML-V2.35-…xml for the engineering tool
```

Import the GSDML into the engineering tool (TIA Portal: *Options →
Manage general station description files*) and add the station. The
GSDML and the driver come from the same manifest, so the PLC is
configured with exactly the layout the driver serves. If they differ
anyway, because the manifest changed and the GSDML was not re-imported,
the device answers the connection with a module difference. The status
row then says which modules differ, and their data is marked invalid
both ways.

## Privileges and the network

PROFINET runs on raw Ethernet, so the process needs `CAP_NET_RAW`:

```sh
sudo setcap cap_net_raw+ep "$(command -v naut)"
# or, under systemd:  AmbientCapabilities=CAP_NET_RAW
```

Without it the project still loads, checks and runs. The driver's status
row says what is missing and the driver keeps retrying. The connection
setup (DCE/RPC) uses UDP 34964 on the host's IP stack, so the interface
needs an address on the PROFINET subnet.

**nautilus never changes the host's network settings.** A PLC assigns its
devices' IP addresses over DCP, and DCP has no authentication.
- An address assignment is accepted only when it names an address the
  interface already holds. Give the device that address in the PLC's
  project.
- Any other assignment is refused, and the status row shows the refusal
  with the addresses the interface has.
- A station name the PLC assigns is used until restart and never saved.
  The manifest stays the source of truth.

## Quality and the online tag

`profinet_device__Online` (or `<driver name>__Online`) is true while a PLC
holds an AR and its frames arrive.

The input tags (the PLC's outputs) carry quality:

| Quality | When |
| --- | --- |
| NotConnected | Until the first good value. The tag reads its `init:`, as for every communications driver. |
| Good | The PLC is in run and its status for the subslot is good. |
| Bad | The PLC is in STOP, its status for the subslot is bad, or the module differs from the layout. |
| Stale | The connection ended: the PLC stopped sending, released it, or the cable is out. |

The last good value is held through Bad and Stale.

The data nautilus provides is marked **invalid** to the PLC (IOPS bad)
in three cases:
- until the program has written every output tag once;
- while the write gate is closed, for example when scans hang;
- after the gate reopens, until every output has been written again.

The PLC sees invalid data rather than zeros or a frozen value, and
handles it as it would a faulty module.

## What it does not do yet

- Run with `redundancy:`. It is refused at load, because a standby has
  to go silent on the PROFINET network and the hand-over is still being
  designed.
- Be a PROFINET **controller** that drives devices. That comes next.
- IRT, MRP, system redundancy, PROFIsafe, more than one AR, alarms raised
  to the PLC, strings or arrays.
- Cycle times under 8 ms until measured on the target.

The design brief is
[docs/design/profinet.md](https://github.com/joyautomation/nautilus/blob/main/docs/design/profinet.md).
