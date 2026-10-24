---
title: Opto 22 groov EPIC (experimental)
description: Run the nautilus runtime on a groov EPIC processor, or drive its I/O from another machine — the chassis imported over OptoMMP, the program in IEC text, nothing of Opto's to license or install.
---

:::caution[Experimental]
Verified on one bench unit: a GRV-EPIC-PR1 on firmware 4.1.x with four
modules (24 DI, 12 DO, 12 ICTD, 24 voltage inputs). The commands and the
generated files may change shape between releases. Nothing here makes
nautilus a safety controller, and Opto 22 does not support a third-party
runtime on its processor; read [What it does not do yet](#what-it-does-not-do-yet)
before pointing it at a chassis that matters.
:::

A groov EPIC is a Linux controller with its I/O on the same rack. Opto 22
opens that Linux (a free shell licence) and documents the protocol its
own software uses to reach the I/O, **OptoMMP** on TCP 2001. That is
enough for nautilus to be the runtime: the chassis is read over OptoMMP,
the program is ordinary nautilus source, and the same static binary that
runs on a laptop runs on the processor.

There is no lower path to the I/O to want. The modules sit on the
EPIC's backplane behind Opto's own I/O server, and every program on the
processor reaches them as a client of that server, Opto's PAC Control
engine and its CODESYS runtime included. nautilus is one more client,
on loopback, at the same level as the vendor's own runtimes.

## What you need

- A groov EPIC processor with its modules configured in groov Manage
  (module and channel types are read from there, not typed).
- For the runtime **on** the EPIC: Opto's free **GROOV-LIC-SHELL**
  licence (groov Manage → System → License), then a shell user and the
  shell server started on the Shell Access page. The shell user is its
  own account, not the groov Manage admin, and nothing below needs its
  sudo.
- `naut` for ARMv7: the `linux_armv7` archive from the
  [release page](https://github.com/joyautomation/nautilus/releases/latest),
  or `CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build ./cmd/naut`.

## Import the chassis

From any machine that can reach the processor:

```sh
naut optommp browse --host <epic>   # every module and configured channel: part, channel type, unit, live value
naut optommp import --host <epic>   # writes the driver manifest + tags/optommp.yaml
```

`browse` reads what the processor itself reports: module type and channel
type per slot, the channel-present mask, the current values. `import`
turns that into a manifest and a tag file, one tag per channel, named
`<id>_M<slot>_<Kind><ch>` (`EPIC_M1_DO11` is slot 1, digital output 11).
Outputs are writable bindings. Re-run it after changing a module or a
channel type in groov Manage; the output is byte-identical when nothing
changed, so the diff shows exactly what moved.

Today the import binds every channel to the processor's own Modbus/TCP
server (form 1678's fixed map: 64 channels per module, digital state at
coil/discrete `512 + slot*64 + ch`, analog values as float32 at input
register `4608 + (slot*64 + ch)*2`), and the project uses the ordinary
[Modbus driver](/guides/modbus/):

```yaml
driver:
  type: modbus
  manifest: modbus_manifest.yaml
  scan-rate: 100ms
tag-files:
  - tags/optommp.yaml
```

## Run it on the processor

```sh
scp naut <shell-user>@<epic>:nautilus/naut
scp -r my-project <shell-user>@<epic>:nautilus/
ssh <shell-user>@<epic> 'cd nautilus/my-project && ../naut run .'
```

The manifest's host can stay the EPIC's address or become `127.0.0.1`;
both reach the same server. Measured on the bench unit with the
`epic-bench` example (11 inputs mirrored to 11 outputs, a 1 Hz heartbeat,
77 tags, 100 ms scan): **4 % of one core and 17 MB resident**, with
Opto's Ignition Edge and PAC Control engine still running beside it.
Opto's own software is not touched, and nothing is installed outside the
shell user's home directory.

Bind the server to `localhost` (the default in the example) and reach the
dashboard through an SSH tunnel, or give it the EPIC's address on a
network you trust; the dashboard and tag API have no login of their own.

## Unwired channels read NaN

The EPIC reports an analog channel with nothing connected (an open ICTD
probe, for one) as **NaN**. nautilus renders a non-finite REAL as JSON
`null`, and the driver marks that tag's quality `bad`: the link is fine,
the reading is not. Logic that compares an analog value should check its
quality first, as it would for any field device.

## What it does not do yet

- **Per-channel quality beyond NaN.** The Modbus projection carries the
  value only. OptoMMP has a channel-present mask and per-channel quality
  codes; a native OptoMMP driver that carries them is in progress.
- **Start on boot.** The bench runs are started over SSH. A user-level
  service for the shell user is the next step; until then, a reboot of
  the processor leaves the runtime stopped.
- **Channel names from groov Manage.** Tags are named by slot and
  channel; the names configured in groov Manage are not reachable over
  OptoMMP.
- **groov RIO and SNAP PAC.** Both answer OptoMMP and should import the
  same way; neither has been tried.
- **Support.** Opto 22 supports its own runtimes on its processor. A
  third-party runtime is yours (or your integrator's) to support.

## Where the evidence is

The `examples/epic-bench` project in the repository is the bench project,
with its acceptance suite (`naut test .` passes with no EPIC in sight).
`docs/design/open-controllers.md` has the measurements, the memory map
used, and what Opto's own CODESYS driver turned out to talk to.
