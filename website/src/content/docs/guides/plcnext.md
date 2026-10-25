---
title: Phoenix Contact PLCnext (experimental)
description: Run the nautilus runtime on a PLCnext controller and drive its Axioline I/O — no PLCnext Engineer project, no SDK, the bus brought up by Phoenix Contact's own open BusConductor component.
---

:::caution[Experimental]
Verified on one bench unit: an AXC F 2152 on firmware 2023.0 LTS with four
Axioline F modules. The driver, the manifest and the controller-side files
may change shape between releases, and the firmware's behaviour described
here was measured on that one version. Nothing here makes nautilus a
safety controller. Read [What it does not do yet](#what-it-does-not-do-yet)
before pointing it at a controller that matters.
:::

A PLCnext controller is a PREEMPT_RT Linux box with an Axioline bus, and
Phoenix Contact publishes the pieces needed to run something other than
its own IEC engine on it. nautilus uses those pieces: the controller runs
the same static `naut` binary as a laptop, Phoenix Contact's
[BusConductor](https://github.com/PLCnext/BusConductor) configures the bus
at start, and the driver exchanges the process image through the
firmware's own data-access service. PLCnext Engineer is never opened, and
no IEC program of Phoenix Contact's runs.

## What you need

- A PLCnext controller with its Axioline modules on the local bus, and
  SSH as `admin` (on by default).
- BusConductor, from its upstream repository, plus three controller-side
  files from this repository's `examples/plcnext-bench/controller/`.
- `naut` for ARMv7: the `linux_armv7` archive from the
  [release page](https://github.com/joyautomation/nautilus/releases/latest),
  or `CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build ./cmd/naut`.

## How the I/O reaches nautilus

Three facts about the firmware decide the shape. Each was found by trying
it, on 2023.0:

- The data-access service resolves **MainProcess ports only** (eCLR
  variables, ESM, components such as BusConductor's). The I/O
  components' own ports answer `DAE_NotExists` under every spelling, so
  I/O reaches a service only through a **GDS connector** into a port the
  service can see.
- It reads and writes **array elements by name** (`Port[17]`) and refuses
  a whole array. The driver reads the bytes its bindings reach as one
  multi-item read per scan and flushes the output span as one write.
- BusConductor's requests act on a **rising edge sampled every 100 ms**.
  The driver holds each request low, raises it, leaves it high, and skips
  a bus that is already configured and running, so a reconnect never
  restarts good I/O.

The connector needs no Engineer project: a bare controller's eCLR exposes
two 512-byte PROFINET-device arrays, `Arp.Plc.Eclr/PND_S1_INPUTS` and
`PND_S1_OUTPUTS`, which are unused on a controller that is not a PROFINET
device. The example's `PCWE.gds.config` re-points those two connectors at
the Axioline images, and the driver's defaults read and write the mirrors.

## Set up the controller

BusConductor goes in as its own project directory, so the factory project
is untouched and rollback is one symlink:

```sh
git clone https://github.com/PLCnext/BusConductor /tmp/bc
mkdir -p /tmp/bcp && cp -r /tmp/bc/generic_axioline/. /tmp/bc/no_engineer/. /tmp/bcp/
cp examples/plcnext-bench/controller/PCWE.gds.config   /tmp/bcp/Plc/Gds/
cp examples/plcnext-bench/controller/pcwe.plm.config   /tmp/bcp/Plc/Plm/
cp examples/plcnext-bench/controller/PCWE.opcua.config /tmp/bcp/Services/OpcUA/   # optional
tar czf bc.tgz -C /tmp/bcp . && scp bc.tgz admin@<controller>:/opt/plcnext/

ssh admin@<controller>
mkdir -p /opt/plcnext/projects/BusConductor
tar xzf /opt/plcnext/bc.tgz -C /opt/plcnext/projects/BusConductor
cd /opt/plcnext/projects && rm current && ln -s BusConductor current
sudo /etc/init.d/plcnext restart          # about a minute
```

`admin`'s sudo is an allowlist: the runtime's init script is on it, and
the projects directory is admin-owned, so the symlink needs no sudo.
**Rollback:** point `current` back at `PCWE` and restart.

## Run nautilus

```sh
scp naut admin@<controller>:/opt/plcnext/apps/nautilus/naut
ssh admin@<controller> '/opt/plcnext/apps/nautilus/naut plcnext browse'
```

`browse` brings the bus up the way the driver does (idempotently), prints
`CONFIGURED`, `NUM_MODULES` and the bus `RUN` bit, and dumps the first
bytes of both images, so a manifest's offsets can be checked against the
modules actually there. Then the project:

```yaml
driver:
  type: plcnext
  manifest: plcnext_manifest.yaml
  scan-rate: 50ms
```

The manifest lists the modules in bus order and binds tags to byte
offsets and bit or integer formats; `naut plcnext tags` writes the tag
file from it. In BusConductor's generic mode **every module takes its
frame length in both images**, in bus order: a DO16 takes two bytes of
the input image it never writes. The example's manifest has the measured
layout for its four modules.

`naut plcnext read <port>` and `naut plcnext write <port> <value>` read
or write any GDS name the service will answer for, which is the way to
learn whether a name exists before a manifest binds it.

Measured on the bench unit with the `plcnext-bench` example (16 inputs
mirrored, a 1 Hz heartbeat, 50 ms scan): **9 % of one core and 23 MB
resident**. Each scan's exchange is one round trip over the local
socket, about 11 ms for this manifest. That is remote-I/O-grade timing,
the same tier as an Engineer program reading the GDS from a
non-real-time thread.

## Faster: the in-process tier

The firmware refreshes the Axioline images every 500 µs. To reach them at
that rate, the repository has `rt/plcnext-io`: a small C process that the
firmware's own application framework starts, which exchanges the image
inside its own cycle through the ANSI-C API the firmware ships, the way
Phoenix Contact's `SampleRuntime` does, built without the PLCnext SDK.
It runs on the bench at a 1 ms cycle. Handing its image to the nautilus
runtime through shared memory is the next step. Until it lands, treat it
as a design preview, described in `docs/design/open-controllers.md`.

## What it does not do yet

- **Per-channel quality.** The driver has one quality for the whole
  image: the bus `RUN` bit. False makes every input `bad`.
- **Start on boot.** The bench runs are started over SSH; a start-on-boot
  recipe is the next step.
- **Import from a bus scan.** The manifest is written by hand from
  `browse`; a `naut plcnext import` is planned.
- **Analog modules without their supply.** An Axioline module whose UP
  supply is not wired reports Axioline's supply-missing marker (`0x8020`)
  as its reading; the driver passes it through as a value.
- **Other firmware versions.** The three facts above were measured on
  2023.0 LTS.
- **Support.** Phoenix Contact supports its own runtime. A third-party
  runtime on the controller is yours (or your integrator's) to support.

## Where the evidence is

`examples/plcnext-bench` in the repository is the bench project, with the
controller-side files and an acceptance suite (`naut test .` runs with no
controller). `docs/design/open-controllers.md` has the measurements and
the boot log lines that show each step working.
