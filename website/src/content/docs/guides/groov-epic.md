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
turns that into a driver manifest and a tag file, one typed tag per
channel (BOOL for digital, REAL for analog, so a program reads them
without `VAR_EXTERNAL`), named `<id>_M<slot>_<Kind><ch>`
(`EPIC_M1_DO11` is slot 1, digital output 11). Outputs are writable bindings. Re-run it after changing a module or
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
trip when nautilus alone stops. A redundant standby is not one of those
other clients: with `watchdog:` set it does not talk to the unit at all
(see [Redundancy](#redundancy)).

Nor does it trip when nautilus's scan hangs (logic that never returns, a
driver call that blocks) without `redundancy:`. The driver's own loop
keeps polling the unit, and those polls feed the watchdog, so the outputs
hold their last command for as long as the hang lasts. `naut run` gates
some drivers on live scans in every project, but not this one, because
it reads any gate as "one replica of a redundant set" (see
[Redundancy](/guides/redundancy/#redundancy)). With `redundancy:`, a hung
leader closes its gate and hands over (see [Redundancy](#redundancy)
below).

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
driver that has them (`<id>__Online` is what the Modbus, EtherNet/IP,
SNMP and Sparkplug host drivers deliver too, and the Modbus and Sparkplug
host drivers deliver `<id>__Outages` as well), so the logic does not
change with the driver.

The EPIC's own I/O is local I/O, so a channel input the driver has not
delivered yet still faults the scan that reads it (`undefined tag`). The
protocol drivers' inputs read as zero until delivered instead; see
[Inputs from a device that is
down](/guides/tag-model/#inputs-from-a-device-that-is-down). The counters only go up, so a program that compares
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

## Redundancy

With a `redundancy:` section, the driver's write gate is wired to
leadership: commands leave the driver only on the leader. A standby skips
scans, so it never hands the driver a command, and its driver neither
rewrites outputs on reconnect nor after a watchdog trip.

**A standby changes nothing on the unit.** Unless the manifest sets the
watchdog (below), it keeps a session with the processor and polls the
packed areas, whatever the manifest asks for: the plain reads a polled
scan makes (two on an ordinary chassis). It leaves
the unit's configuration to the leader:
it does not send the power-up clear, set the watchdog, write the custom
data area's pointers, or configure the stream. It clears no latch: a
latch reads set on the standby until the leader, which must see the
event, delivers and clears it. At the takeover edge a latch event can
still be lost, when the old leader clears it in its last scan before it
sees the gate close and its program never runs on it, or delivered twice,
when both replicas lead for a moment while the lease changes hands. A
standby's clean stop puts nothing back.

**With `watchdog:` set, a standby does not talk to the unit.** Any
client's OptoMMP request resets the unit's communication watchdog, so a
polling standby would keep it fed. Take a leader that still holds the
lease but cannot reach the unit (a network fault between the two, both
still reaching the cluster): its outputs never arrive, and a standby that
polled would keep the watchdog from tripping, so the unit would hold the
last command for as long as the fault lasted. So while the manifest sets
`watchdog:` above `0s`, a standby holds no connection to the processor:
no request and no stream. (A gate that closes in the instant between the
driver's look before it connects and its next look can still get a TCP
connect, closed again before any request.) If the leader loses the
unit, nothing else feeds the watchdog, and it trips and drives the outputs
to their watchdog values, as it would for a single controller
([#626](https://github.com/joyautomation/nautilus/issues/626)). The cost
is the standby's live view of that unit: its tags are Stale (or not
connected, if it never led), `online` is false without counting an
outage, and the status row reads "Standby: not polling" (Health mode
`standby (watchdog)`). Other drivers on the same standby keep polling
their devices. Without `watchdog:`, or with `0s`, a standby polls as
above.

**A watchdog the manifest does not name is still fed.** The driver only
knows what the manifest says. A watchdog set in groov Manage, or left
armed after you delete `watchdog:` from the manifest (nautilus never turns
it off unless the manifest says `0s`), is fed by a polling standby, so a
leader that loses the unit will not let it trip. The standby reads the
unit's watchdog each time it starts standing by. When it is on and the
manifest does not set it, the driver logs a warning and the status row
turns degraded: "Standby polling feeds the unit's … ms watchdog, which the
manifest does not set". Name the watchdog in the manifest.

**A watchdog standby's takeover waits for the unit.** When the gate opens
the driver connects at once (it looks at the gate every scan while
standing by, and the runtime asks it on every scan of the takeover). It
leads as below: power-up clear if owed, watchdog, custom data area and
stream. Until its first scan of the unit has been delivered, it tells the
runtime its inputs are not live, and the runtime holds the takeover: no
frame reset, no logic, and no outputs from any driver, not just this one.
Its scan loop keeps ticking meanwhile, so the replica stays live for the
lease. The first scan that runs is a warm start on the unit's values, not
on what the standby last saw. The wait is bounded (2 s by default), and
one hiccup in the dial can use it up: a failed first connect retries
after about 1 s, and a lost SYN costs as much. Past the wait the takeover
runs on the inputs the drivers hold, with a warning: the logic and every
other driver's outputs run on stale inputs, while this driver still
sends no command until the runtime has read a scan of
the unit, then asks for every output, so the first command it sends comes
from fresh inputs. On a reachable unit the takeover costs the TCP connect
and the session's set-up requests (a dozen or so, plus one per bound
output for its watchdog value, at about 0.75 ms each) beyond a polling
standby's. If a sibling driver's read fails on that
first scan, the scan runs on last-known values, as any failed read does.

**A takeover moves the stream.** The role follows the write gate, looked
at every scan and every datagram. A standby whose gate opens becomes the
leader on the same connection, with no outage: it sends the power-up clear
if one is owed and configures the watchdog, the custom data area and the
stream as the manifest asks, pointing the stream at itself. A leader whose
gate closes stops using the stream and polls as a standby (with
`watchdog:` set, it closes its connection instead); it leaves the
configuration where it is, since the new leader now owns it. Only a
replica that still leads when it stops puts the stream and the pointers
back: one that lost the lease and is stopped before it looks again
leaves them to the new leader.

**After a takeover, a stop cannot put back the original configuration.**
Each replica records the stream and the custom data area's pointers as it
first found them, in memory. Only the replica that led first found the
unit as it was before nautilus; one that takes over finds the previous
leader's settings. So what the last leader's stop puts back depends on
which replica that is, and a stream the unit had before nautilus (to a
historian, say) is put back only if the replica that first led is the
one that stops last. Under redundancy a stream of the custom data area
found at a takeover (the previous leader's) is recorded as off, so a stop
never re-points the stream at the other replica. Note the unit's stream
settings before you deploy a redundant pair, and put them back by hand in
groov Manage if you retire it.

**A command held while the processor was away goes out only on the
leader.** Leadership is checked again when held commands go out, before
every write. A replica that lost leadership during the outage drops what
it held instead of writing it once the processor is back. A flush the gate
closes partway through stops there: the write in hand and every one after
it are dropped. The dropped commands are asked for again, so the push that
follows the reopen sends them.

**When the gate reopens, every output goes out again.** While it was
closed another replica may have led and written the same outputs, so
nothing this replica commanded before is trusted to still be there. The
first time the driver sees the gate open again it forgets every command,
and what is still held from before, and asks the runtime for the outputs.
The next push writes every output with the program's current value,
including one that has not changed since this replica last led. A replica
that regains leadership pushes because it took over. A gate that reopens
without a takeover (logic healthy again) pushes because the driver asked.
Until that push the reconnect and watchdog rewrites have nothing to write,
so a command from the previous term is never sent.

**A replica that started as a standby commands every output on its first
takeover.** Like a plain start, the first value handed over for each
output is written (see [What a scan does](#what-a-scan-does)).

**A flip between two looks is not seen.** A gate func cannot say it
changed, so the driver looks at it: on every push and flush, before every
write in a flush, every scan (every stream check when streaming) and at
least once a second while connected, every scan while a standby with
`watchdog:` set waits unconnected, and on every connect attempt. A gate
that closes and reopens between two looks is not seen, and then nothing
above happens. The window is about a second, or a scan when that is
shorter, while connected, and up to the reconnect backoff (30 s) while
the processor is away.

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
