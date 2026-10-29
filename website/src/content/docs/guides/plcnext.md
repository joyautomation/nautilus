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
at start, and the driver exchanges the process image either through the
firmware's own data-access service or, faster, through a small C process
the firmware starts beside its own (the [in-process
tier](#faster-the-in-process-tier)). PLCnext Engineer is never opened, and
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
the input image it never writes. A digital module numbers its channels
byte by byte, while `bit:N` counts in a big-endian 16-bit word, so
channel n of a 16-channel module binds as `bit:((n+7) mod 16)` (channel
01 is `bit:8`). The example's manifest has the measured layout for its
four modules. Check a new layout with eyes on one output LED and one
input before trusting it: the bytes alone do not say which module is
which.

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

The firmware refreshes the Axioline images every 500 µs; a gRPC round
trip takes about 11 ms. To reach the images at the bus rate, the
repository has `rt/plcnext-io`: a small C process, `nautilus_io`, that
the firmware's own application framework starts, which reads and writes
the Axioline frames every millisecond through the ANSI-C API the
firmware ships (the way Phoenix Contact's `SampleRuntime` does, built
with zig and no PLCnext SDK). It shares them with naut through a
shared-memory page. Each CLI release attaches it built, as
`nautilus-plcnext-io_<version>_linux_armv7.tar.gz` (the binary, its two
ACF files and the README); deploy it as `rt/plcnext-io/README.md` says,
or build it yourself with `rt/plcnext-io/build.sh`. Once it runs, it is the output frame's only writer, so the
project's `PCWE.gds.config` must have no connector into `DO4096`. Then:

```yaml
# plcnext_manifest.yaml
transport: shm           # default grpc
output-watchdog: 1s      # the default; "0" holds the last outputs
```

The bindings and the tag file do not change; only the bytes' route
does. The BusConductor handshake still goes over gRPC. Every scan the
driver copies the inputs out of the page and its outputs into it; the
shim writes the frame on its next cycle. If naut stops refreshing the
page (a crash, a kill, a redeploy), the shim drives the output frame to
zero once naut's outputs are older than `output-watchdog`. If the
runtime restarts, the shim does too, and the driver notices the new
start and runs the handshake again.

Measured on the bench unit with `plcnext-bench` at a 50 ms scan:

| | `transport: grpc` | `transport: shm` |
|---|---:|---:|
| time per scan for the image | ~11 ms | 0.03–0.05 ms |
| input age when the program reads it | the RPC plus up to 500 µs | 0.1–1.1 ms |
| naut CPU (of one core) | 9 % | 4 % |
| naut resident | 23 MB | 20 MB |
| shim CPU / resident | — | ~3 % / 17 MB |

A 5 ms scan works on shm too, at 21.5 % of a core for naut.

`naut plcnext shm` prints the page: whether the PLC has started, the
bus `RUN` bit, the shim's cycle, priority and CPU, its lateness counters,
and both frames with who wrote them and how fresh they are. `--watch
250ms` prints a line per interval, which is the way to see an output
toggle without a meter on the terminals. `naut plcnext bench` measures
one window of the shim's lateness and naut's scan lateness and CPU.

### Real-time priority

Out of the box the shim runs best-effort: the firmware's own process
holds the Linux capability to ask for `SCHED_FIFO`, and the processes it
starts do not inherit it. Pin the shim to CPU 1 (`echo 1 >
io/nautilus_io.cpu` beside the binary) and naut to CPU 0: CPU 0 carries
the firmware's periodic real-time tasks, and naut's threads sharing the
shim's core were most of its lateness.

To run the shim at `SCHED_FIFO`, the controller's owner gives both
binaries the `cap_sys_nice` file capability once, as root
(`rt/plcnext-io/README.md` has the one-line recipe, which sets a root
password, uses it, and locks root again). **The capability belongs to
the file: copying a new build into place drops it**, and the step is
needed again. Without it both programs run best-effort and say so in the
shim's log, the page, and naut's `scan.sched`. With it the shim asks for
priority 80 (a `nautilus_io.prio` file asks for another), and a naut
task may set `priority:`.

Shim lateness on the bench unit, 3-minute windows, naut at a 50 ms scan
on CPU 0, late meaning more than 200 µs past the shim's 1 ms deadline,
"worst" the worst 10 000-cycle window:

| shim on CPU 1 | quiet | two busy loops | a stream into the Ethernet over ssh |
|---|---:|---:|---:|
| best-effort | 0.13 % late, worst 2.6 ms | 0.20 %, worst 0.95 ms | 33 %, worst 9.6 ms |
| `SCHED_FIFO` 80 | none late, worst 51 µs | none late, worst 154 µs | 3.2 %, worst under 1 ms |

At FIFO 80 nothing was late by more than a millisecond in any run, so the
1 ms cycle holds. The 200 µs line does not hold while someone streams
into the controller's Ethernet: the network receive task outranks the
shim on its core.

## Holding outputs until an operator acknowledges

When the image is fresh again, after a stall, a lost connection or a
restart, the driver drives the outputs to their commands again on its
own. A program that should keep them off until someone has looked can
read the driver's status, bound in the manifest like any other tag:

```yaml
# plcnext_manifest.yaml
  - { name: AXC__Online, status: online }
  - { name: AXC__Outages, status: outages }
  - { name: AXC__WatchdogTrips, status: watchdog-trips }
```

`naut plcnext tags` puts them in the tag file as inputs. `__Online`
(BOOL) is true while the inputs are good: the image is fresh and the
bus runs. `__Outages` (DINT) counts the times it went false.
`__WatchdogTrips` (DINT) counts the times the output frame went to zero
without the driver choosing it: naut stalled for longer than
`output-watchdog` between two writes to the page, the shim reported its
watchdog had fired, or the shim restarted. A naut killed and restarted
by the keepalive finds the shim's watchdog bit set by its predecessor's
outputs, so it starts with `__WatchdogTrips` at 1: the program sees
"restarted after a fault" from its first scans. A first start on a page
no naut has written counts nothing. On `transport: grpc` there is no
output watchdog, and the count stays 0.

The values are there from the first scan, always with good quality, and
named the same way on every driver that has them. The counters only go
up, so a program compares them with last scan's values and seals in a
hold:

```iecst
VAR_EXTERNAL
    AXC__Online        : BOOL;
    AXC__Outages       : DINT;
    AXC__WatchdogTrips : DINT;
    OperatorAck         : BOOL;   (* a setpoint: the HMI's acknowledge button *)
END_VAR
VAR
    held        : BOOL;
    seenOutages : DINT;
    seenTrips   : DINT;
    ackEdge     : R_TRIG;
END_VAR

ackEdge(CLK := OperatorAck);
IF ackEdge.Q AND AXC__Online THEN
    held := FALSE;                      (* released only once the I/O is back *)
END_IF;
IF AXC__Outages <> seenOutages OR AXC__WatchdogTrips <> seenTrips THEN
    held := TRUE;                       (* sealed in on any new outage or trip *)
END_IF;
seenOutages := AXC__Outages;
seenTrips := AXC__WatchdogTrips;

Pump1_Run := Pump1_Cmd AND NOT held;
```

`examples/plcnext-bench` does this for its outputs with
`lib/io_hold.st`, and its `naut test` suite covers the hold, the
acknowledge and the restart case.

## Start on boot

The controller has no systemd, and `admin` cannot write `/etc/init.d`,
but it may keep a crontab, and cron starts before the PLCnext runtime.
`examples/plcnext-bench/controller/naut-keepalive.sh` starts `naut run`
for the project unless it is already running, pinned to the CPU in
`naut.cpu` if that file exists:

```sh
ssh admin@<controller> 'printf "@reboot %s\n* * * * * %s\n" \
  /opt/plcnext/apps/nautilus/plcnext-bench/controller/naut-keepalive.sh \
  /opt/plcnext/apps/nautilus/plcnext-bench/controller/naut-keepalive.sh | crontab -'
```

It starts naut at boot and restarts it within a minute of a crash;
`crontab -r` takes it out. naut need not wait for the runtime: the
driver retries the socket and the page. The shim starts with the
runtime, because the firmware starts it. On the bench, after a reboot
naut, the shim (FIFO 80, capabilities intact) and the heartbeat were all
back about a minute after the controller answered ssh.

## What it does not do yet

- **Per-channel quality.** The driver has one quality for the whole
  image: the bus `RUN` bit. False makes every input `bad`.
- **The status tags on the controller.** `__Online`, `__Outages` and
  `__WatchdogTrips` are tested against an in-process fake of the shim,
  not yet on the bench unit.
- **Holding outputs without a program.** The acknowledge lives in your
  program; there is no manifest setting that keeps the frame at zero
  until one is written.
- **Real-time priority without root.** `SCHED_FIFO` needs the owner's
  one-time root step, and again after every deploy of either binary.
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
controller). `rt/plcnext-io` is the shim, with its build, the exchange
page's layout and the full lateness tables. `docs/design/open-controllers.md`
has the measurements and the boot log lines that show each step working.
