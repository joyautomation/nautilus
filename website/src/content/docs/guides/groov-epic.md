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
  own account, not the groov Manage admin. Running nautilus needs no
  sudo; [starting it on boot](#start-it-on-boot) and
  [real-time priority](#timing-on-the-processors-real-time-kernel) do,
  once.
- `naut` for ARMv7: the `linux_armv7` archive from the
  [release page](https://github.com/joyautomation/nautilus/releases/latest),
  or `CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build ./cmd/naut`.

## Import the chassis

From any machine that can reach the processor:

```sh
naut optommp browse --host <epic>   # every module and configured channel: part, channel type, unit, live value
naut optommp import --host <epic>   # writes optommp_manifest.yaml + tags/optommp.yaml
```

`browse` reads what the processor itself reports: module type and channel
type per slot, the channel-present mask, the current values. `import`
turns that into a driver manifest and a tag file, one tag per channel,
named `<id>_M<slot>_<Kind><ch>` (`EPIC_M1_DO11` is slot 1, digital output
11). Outputs are writable bindings. Re-run it after changing a module or
a channel type in groov Manage; the output is byte-identical when nothing
changed, so the diff shows exactly what moved.

The project uses the `optommp` driver:

```yaml
driver:
  type: optommp
  manifest: optommp_manifest.yaml
  scan-rate: 20ms
tag-files:
  - tags/optommp.yaml
```

The manifest records the chassis as imported (part number and module type
per slot), the binding for each tag, and each channel's type, unit and
range. Optional tags ride on a channel's name:

| flag | adds | type |
|---|---|---|
| (a digital channel with a feature configured) | `_Count`, the counter or totalizer value | DINT |
| `--latches` | `_OnLatch` / `_OffLatch` for every digital input: it changed since the last scan, however briefly | BOOL |
| `--quality` | `_Quality` for every analog channel: the processor's quality code, 0 = good | DINT |
| `--scratch-bits/-ints/-floats N` | writable scratchpad registers, the processor's shared memory with PAC Control, groov View and Node-RED | BOOL / DINT / REAL |

`--modbus` writes the older route instead: every channel bound to the
processor's own Modbus/TCP server (form 1678's fixed map: digital state
at coil/discrete `512 + slot*64 + ch`, analog values as float32 at input
register `4608 + (slot*64 + ch)*2`) for the ordinary
[Modbus driver](/guides/modbus/). It carries values only, no per-channel
quality.

## What a scan does

A scan is the fewest OptoMMP requests the bindings allow, because each
costs about the same (≈ 0.75 ms inside the processor's I/O server)
whatever its size. For the bench chassis that is three: the analog
values, the digital state with its quality mask and latches, and the
scratchpad. Outputs go out when they change: every digital output in one
must-on/must-off write that touches only the commanded channels, so
another client's outputs on the same module are left alone. Outputs are
read back too. An output tag's value is what the program commands, but
a digital output that the module has not switched to that value (another
client, a watchdog trip, a fault) makes its tag **bad** until it does.

The driver checks the chassis against the manifest on connect and every
5 s after (groov modules hot-swap), and reconnects with backoff when the
processor goes away; commands written meanwhile go out when it is back.

## Quality per channel

Every tag carries a quality of its own:

| the channel | quality |
|---|---|
| read this scan, nothing wrong | good |
| on a slot that is empty, or holds a different module than the manifest names | bad |
| not present on its module (the processor's channel-present mask) | bad |
| flagged in the module's quality mask (digital) | bad |
| an analog reading of **NaN** (an open ICTD probe, any unwired analog input) | bad |
| a digital output still not in its commanded state 250 ms (or three scans) after the write | bad |
| the connection is down, or a stream is late | stale |
| never read since start | not connected |

A NaN renders as JSON `null` on the tag API and stream: the link is fine,
the reading is not. Logic that compares an analog value should check its
quality first, as it would for any field device. The processor's own
quality code for an analog channel is there too, as the `_Quality` tag
(`--quality`), for logic that wants the reason.

## Poll, custom area or stream

The import writes a polling manifest, because the other two modes write
the processor's own configuration. One line in `optommp_manifest.yaml`
changes it (or re-import with `--custom-area` / `--stream 20ms`):

- `custom-area: true` maps every word the scan needs into the processor's
  custom data area, so a scan is one read;
- `stream: 20ms` has the processor push that area to nautilus over UDP
  every 20 ms. nautilus then sends only writes, latch clears and the
  chassis check. Five missed intervals make every tag stale until the
  next datagram.

Measured on the bench EPIC (2026-10-24), `naut` on the processor, 20 ms
task, one minute per row, CPU from `/proc`:

| inputs | naut CPU | RSS | I/O server CPU | per scan |
|---|---|---|---|---|
| packed poll, 20 ms | 13.7 % | 20.7 MB | 12.4 % | 3 requests, 1.62 ms |
| custom area, 20 ms | 12.0 % | 19.7 MB | 8.4 % | 1 request, 0.53 ms |
| stream, 20 ms | 10.0 % | 19.5 MB | 8.3 % | none; longest gap 25.7 ms |
| stream, 5 ms (task 5 ms) | 20.5 % | 21.5 MB | 11.1 % | none; longest gap 24.1 ms |

(The I/O server idles at about 6.6 %.) Two things to know before turning
either on:

- **The processor has one stream configuration and one set of
  pointers.** nautilus owns them while it runs, warns when it finds them
  in use, and rewrites them if another client changes them. Two runtimes
  that both use the custom area on one EPIC (nautilus and CODESYS, say)
  will fight.
- **The stream configuration is stored to flash.** A clean stop (Ctrl-C,
  SIGTERM) puts the pointers and the stream back as nautilus found them;
  a `kill -9` or a power cut leaves the processor streaming to a port
  nobody reads until nautilus next starts.

**Where the pointers point matters.** A custom-area pointer into the
processor's *packed* analog area costs its I/O server about 0.5 ms every
time it is resolved: 36 of them streamed every 20 ms held it at 90 % of a
core. Pointers into the per-channel areas return the same values, NaN
included, for next to nothing, and that is what nautilus writes. If
something else on your EPIC sets up pointers, check which area they
name.

## The communication watchdog

```yaml
watchdog: 500ms
```

sets the I/O unit's communication watchdog: after 500 ms without an
OptoMMP request, every output given a watchdog value in groov Manage goes
to it. Which outputs, and to what, stays groov Manage configuration; the
status row shows how many bound outputs have one. `0s` turns it off;
leaving the key out leaves the processor's setting alone. A polling
manifest refuses a watchdog shorter than three scans, and while streaming
the driver reads
at a third of the watchdog time to keep it fed. It stays set after
nautilus stops, which is the point.

A tripped unit leaves its outputs at their watchdog values until
something writes them, and nautilus otherwise writes an output only when
its command changes. So it rewrites every digital and analog output it
commands whenever the unit may have gone longer than the watchdog time
without a request: a stalled process, a network outage TCP rode out, a
scan that overran. It counts conservatively, from one request's send to
the next one's answer, and logs a warning once each time it happens. A
reconnect rewrites every commanded output, scratchpad included, because
the processor may have restarted meanwhile.

The watchdog is per unit and any client's request resets it, so on an
EPIC where groov Manage or another program also talks OptoMMP it may not
trip when nautilus alone stops.

## Holding outputs until an operator acknowledges

Recovery is automatic: when the processor comes back, or after a
watchdog trip, the driver puts every output back to its command, the
way a standard PLC does. If a program should instead keep its outputs
off until someone has looked, it can, because the driver reports its
own status as three input tags. `naut optommp import` binds them on
every import:

| tag | type | what it is |
|---|---|---|
| `EPIC__Online` | BOOL | the driver is exchanging data with the processor now (a session is up and, when streaming, the stream is not late) |
| `EPIC__Outages` | DINT | times `EPIC__Online` went false since nautilus started |
| `EPIC__WatchdogTrips` | DINT | times the unit's watchdog may have driven outputs to their watchdog values: the driver went quiet for longer than the watchdog (the same check that triggers the rewrite above), or reconnected after a gap longer than it |

```yaml
# optommp_manifest.yaml (written by the import)
  - { name: EPIC__Online, point: online }
  - { name: EPIC__Outages, point: outages }
  - { name: EPIC__WatchdogTrips, point: watchdog-trips }
```

They are delivered from the first scan, before the processor answers,
and their quality is always good. The names end the same way on every
driver that has them (`<id>__Online` is what the Modbus, SNMP and
Sparkplug host drivers already deliver), so the logic does not change
with the driver. The counters only go up, so a program that compares
them with last scan's values never misses one. A seal-in that releases
on an acknowledge:

```iecst
VAR_EXTERNAL
    EPIC__Online        : BOOL;
    EPIC__Outages       : DINT;
    EPIC__WatchdogTrips : DINT;
    OperatorAck         : BOOL;   (* a setpoint: the HMI's acknowledge button *)
END_VAR
VAR
    held        : BOOL;
    seenOutages : DINT;
    seenTrips   : DINT;
    ackEdge     : R_TRIG;
END_VAR

ackEdge(CLK := OperatorAck);
IF ackEdge.Q AND EPIC__Online THEN
    held := FALSE;                      (* released only once the I/O is back *)
END_IF;
IF EPIC__Outages <> seenOutages OR EPIC__WatchdogTrips <> seenTrips THEN
    held := TRUE;                       (* sealed in on any new outage or trip *)
END_IF;
seenOutages := EPIC__Outages;
seenTrips := EPIC__WatchdogTrips;

Pump1_Run := Pump1_Cmd AND NOT held;
```

`examples/epic-bench` does this for its lamps with a function block,
`lib/io_hold.st`, switched on by a `HoldAfterOutage` setpoint (off at
start, so the bench recovers on its own unless asked not to), and tests
it with `naut test`. To hold after every
start of nautilus as well, add `FIRST_SCAN()` to the seal-in condition:
a watchdog that fired while nautilus was not running is something the
driver cannot see. A trip is counted only when the unit's watchdog is
on, and whether an output actually moved depends on which outputs have a
watchdog value in groov Manage, so the count means "may have".

## Run it on the processor

```sh
scp naut <shell-user>@<epic>:nautilus/naut
scp -r my-project <shell-user>@<epic>:nautilus/
ssh <shell-user>@<epic> 'cd nautilus/my-project && ../naut run .'
```

The manifest's host can stay the EPIC's address or become `127.0.0.1`
(`--manifest-host 127.0.0.1`); both reach the same server at the same
cost. Measured on the bench unit with the `epic-bench` example (11
inputs mirrored to 11 outputs, a 1 Hz heartbeat, 73 tags, polling), with
Opto's PAC Control engine, groov View and Node-RED still running beside
it:

| driver + task scan | naut CPU (one core) | RSS | scan p50 | scan max |
|---|---|---|---|---|
| 100 ms | 3.9 % | 19.2 MB | 1.77 ms | 2.94 ms |
| 20 ms | 14.7 % | 20.9 MB | 1.70 ms | 3.42 ms |
| 5 ms | 35.3 % | 21.3 MB | 2.20 ms | 5.20 ms |

A poll is ≈ 1.7 ms of waiting on the processor's I/O server whatever the
scan rate, so 5 ms is the floor for polling; streaming is the way under
it. Opto's own software is not touched, and nothing is installed outside
the shell user's home directory.

Bind the server to `localhost` (the default in the example) and reach the
dashboard through an SSH tunnel, or give it the EPIC's address on a
network you trust; the dashboard and tag API have no login of their own.

## Start it on boot

The shell user has sudo with its password. One systemd unit, once:

```ini
# /etc/systemd/system/naut.service
[Unit]
Description=nautilus runtime
After=network.target mmp-server.service
Wants=mmp-server.service

[Service]
User=<shell-user>
WorkingDirectory=/home/<shell-user>/nautilus/my-project
ExecStart=/home/<shell-user>/nautilus/naut run .
Restart=always
RestartSec=2
AmbientCapabilities=CAP_SYS_NICE
TimeoutStopSec=10

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload && sudo systemctl enable --now naut
```

systemd stops it with SIGTERM, which is a clean stop: a stream or custom
area is put back. `AmbientCapabilities` lets `priority:` work (next
section) and survives replacing the binary, which a `setcap` on the file
does not.

## Timing on the processor's real-time kernel

The EPIC runs a `PREEMPT_RT` kernel (`5.15.91-rt57` on firmware
4.1.2.0). A task's `priority:` and the driver manifest's `priority:` put
them at `SCHED_FIFO`; both need `CAP_SYS_NICE` (the unit above, or
`sudo setcap cap_sys_nice+ep naut`). Keep both under the I/O server's own
threads at 30:

```yaml
# nautilus.yaml
tasks:
  - program: bench.st
    scan: 20ms
    priority: 20
```

```yaml
# optommp_manifest.yaml
scan-rate: 20ms
priority: 25
```

Measured on the bench EPIC (2026-10-25): `epic-bench`, polling, 20 ms
task and 20 ms driver scan, Opto's software running beside it. Each row
is 5 minutes (≈ 15,000 scans) after a 10 s warm-up; "loaded" is four
busy-loop shells at normal priority, one per core. Task lateness comes
from the runtime's own histogram, so values are bucket ranges; the
driver's wake-up lateness and scan time come from its status row.

| task | driver | load | task p99 / max late | driver wake p99 / max late | driver scan p99 / max | naut CPU |
|---|---|---|---|---|---|---|
| normal | normal | quiet | 0.2–0.5 / 5–10 ms | 1–2 / 9.96 ms | 2–5 / 10.3 ms | 14.5 % |
| FIFO 20 | normal | quiet | 0.1–0.2 / 0.2–0.5 ms | 1–2 / 16.2 ms | 2–5 / 13.9 ms | 13.1 % |
| FIFO 20 | FIFO 25 | quiet | 0.1–0.2 / 0.2–0.5 ms | 0.1–0.2 / 5.54 ms | 2–5 / 24.8 ms | 16.5 % |
| normal | normal | loaded | 0.2–0.5 / 2–5 ms | 1–2 / 6.44 ms | 2–5 / 14.6 ms | 13.4 % |
| FIFO 20 | normal | loaded | 0.1–0.2 / 0.5–1 ms | 1–2 / 5.10 ms | 2–5 / 13.0 ms | 14.7 % |
| FIFO 20 | FIFO 25 | loaded | 0.1–0.2 / 0.5–1 ms | 0.1–0.2 / 1.75 ms | 2–5 / 7.43 ms | 18.3 % |

- **Task priority is close to free and takes the tail off.** No scan
  was more than 2 ms late at FIFO 20; at normal priority 3 were on a
  quiet processor and 8 under load.
- **Driver priority makes the driver start on time**: wake-up p99 from
  1–2 ms to under 0.2 ms, worst case under load from 6.4 ms to 1.75 ms.
  It costs 2–4 points of CPU.
- **It does not shorten the scan.** The 2–5 ms p99 is the round trip
  inside Opto's I/O server, the same in every row; streaming removes the
  request instead.

Maxes are since start, warm-up included. One 25 ms driver scan (quiet,
driver at FIFO 25) was not investigated. Every run had zero driver
errors.

## What it does not do yet

- **Channel names from groov Manage.** Tags are named by slot and
  channel; the names configured in groov Manage are not reachable over
  OptoMMP.
- **Counter reset and analog min/max.** `_Count` reads a counter; clearing
  it, and the per-channel min/max the processor keeps, are not exposed.
- **The watchdog on real outputs.** It has tripped on the bench unit
  (2026-10-27). The rewrite after a trip, and the status tags that count
  it, are tested against an in-process fake of the chassis, not yet on
  the bench.
- **Holding outputs without a program.** The acknowledge lives in your
  program; there is no manifest setting that keeps outputs at their
  watchdog values until one is written.
- **groov RIO and SNAP PAC.** Both answer OptoMMP and should import the
  same way; neither has been tried.
- **Support.** Opto 22 supports its own runtimes on its processor. A
  third-party runtime is yours (or your integrator's) to support.

## Where the evidence is

The `examples/epic-bench` project in the repository is the bench project,
with its acceptance suite (`naut test .` passes with no EPIC in sight).
`docs/design/open-controllers.md` §2 has the memory map, every
measurement above with its conditions, and what Opto's own CODESYS
driver turned out to talk to. `NAUT_OPTOMMP_LIVE=<epic> go test ./optommp
-run Live` runs the driver against a real processor (read-only unless
`NAUT_OPTOMMP_LOOPBACK` names a wired output and input).
