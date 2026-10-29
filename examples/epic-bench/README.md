# epic-bench — the office groov EPIC as nautilus I/O

An Opto 22 **GRV-EPIC-PR1** on the bench, four modules in the chassis,
driven by the nautilus runtime over **OptoMMP** — the processor's own
memory-mapped protocol, and the same seam Opto's CODESYS runtime uses to
reach local I/O — first from a laptop, then from the EPIC's own Linux.
Nothing vendor-side was installed on the EPIC: no PAC Control strategy,
no CODESYS. The chassis was *imported*, not typed, and `bench.st` is the
IEC program that drives it.

| slot | module | what it is | nautilus tags |
|---|---|---|---|
| 0 | GRV-IACDCTTL-24 | 24 digital inputs | `EPIC_M0_DI00` … `EPIC_M0_DI23` |
| 1 | GRV-ODCI-12 | 12 digital outputs | `EPIC_M1_DO00` … `EPIC_M1_DO11` (writable, read back) |
| 2 | GRV-IICTD-12 | 12 ICTD temperature inputs, °C | `EPIC_M2_AI00` … `EPIC_M2_AI11` |
| 3 | GRV-IV-24 | 24 voltage inputs, ±160 V | `EPIC_M3_AI00` … `EPIC_M3_AI23` |
| — | scratchpad | float 0 | `EPIC_SP_F0` (writable) |

## How the files got here

```sh
naut optommp browse --host 192.168.82.243   # what the processor reports: modules, channel types, live values
naut optommp import --host 192.168.82.243 --scan-rate 20ms --scratch-floats 1
                                            # → optommp_manifest.yaml + tags/optommp.yaml
```

`naut optommp` reads the chassis the processor itself sees — module type
and channel type per slot, configured scale and units, the
channel-present mask — and writes `optommp_manifest.yaml` (the processor,
the modules as imported, one binding per channel) and the tag file to
compose. The project says `driver: {type: optommp}`. Tag names are
`<id>_M<slot>_<Kind><ch>`; `--latches`, `--quality` and
`--scratch-bits/-ints/-floats` add optional tags (`naut optommp` with no
arguments lists them), and `--modbus` writes the older projection onto
the processor's Modbus map instead, with the same tag names.

What the driver gives that the Modbus path did not: **quality per
channel**. A NaN analog (every unwired ICTD channel) is Bad; so is a
digital channel whose quality bit the processor sets, a channel the
module does not have, and every tag on a slot that is empty or holds a
different module than the manifest names (re-checked every 5 s; groov
modules hot-swap). A dropped socket makes everything Stale; a tag never
read is NotConnected. Outputs are read back, so `EPIC_M1_DO11` shows what
the module is doing, not only what was asked.

## Run it

```sh
naut check .     # validate offline — no EPIC needed
naut test  .     # the acceptance suite in virtual time (epic-bench_test.yaml)
naut run   .     # poll the EPIC for real: dashboard + tag API on http://localhost:8086
```

`bench.st` mirrors the first eleven inputs to the first eleven outputs,
puts a 1 Hz heartbeat on output 11 (`BlinkPeriodS` is an operator
setpoint), copies the two wired analog channels onto `BenchTempC` and
`BenchVoltsV`, and publishes the temperature to scratchpad float 0.
After an outage (the EPIC unreachable, the stream late, or its
communication watchdog possibly tripped) the lamps stay dark until
`BenchAck` is written: the driver's status tags `EPIC__Online`,
`EPIC__Outages` and `EPIC__WatchdogTrips`, which the import binds, go
into the `IoHold` block in `lib/io_hold.st`. That is tested in virtual
time only, not yet on the bench.

A scan is the fewest OptoMMP requests the bindings allow, because each
one costs the same ≈ 0.75 ms inside the processor whatever its size: the
packed analog values for slots 2–3, the packed digital data (state,
quality, latches) for slots 0–1, and the scratchpad float — three
requests, ≈ 1.5 ms. Outputs go out when they change: every digital
output in one must-on/must-off write that touches only the commanded
channels.

Verified 2026-10-24 from a laptop: with `naut run` up, DO11 read straight
off the processor over a second OptoMMP connection every 100 ms went
`1100000111110000001111100001111110000011` — the 1 Hz heartbeat — and
scratchpad float 0 held the ICTD reading (23.0 °C).

## On the EPIC itself

The same project runs *on* the processor. The EPIC is a quad-core ARMv7
(Cortex-A9 class, VFPv3/NEON) running Opto's Yocto Linux 4.1.3 on a
PREEMPT_RT 5.15 kernel with 1.8 GB RAM and 4 GB free on `/`, so the
static ARM build of `naut` runs unchanged:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags='-s -w' -o naut-arm ./cmd/naut
scp naut-arm <shell-user>@192.168.82.243:nautilus/naut
scp -r examples/epic-bench <shell-user>@192.168.82.243:nautilus/
ssh <shell-user>@192.168.82.243 'cd nautilus/epic-bench && ../naut run .'
```

Shell access needs Opto 22's free **GROOV-LIC-SHELL** license (groov
Manage → System → License) and then a shell user created and the shell
server started on the Shell Access page — the shell user is its own
account, not the groov Manage admin. The shell user has sudo with a
password; nothing here needs it.

Verified 2026-10-24 on the bench, `naut run` on the EPIC with the
OptoMMP driver dialing the processor's own address (the manifest's
`192.168.82.243`; `--manifest-host 127.0.0.1` writes loopback, which
costs the same per request), the EPIC's own stack (PAC Control engine,
groov View, Node-RED) still running beside it. DO11 read off the processor
from the laptop every 100 ms went `0011111000001111100000111111000001111100`;
zero driver errors in any run. One minute per row, CPU and RSS of the
`naut` process from `/proc`, the scan time from the driver's status row
sampled once a second:

| driver + task scan | CPU (one core) | RSS | scan p50 | scan max | requests / scan |
|---|---|---|---|---|---|
| 100 ms | 3.9 % | 19.2 MB | 1.77 ms | 2.94 ms | 3 |
| 20 ms (as shipped) | 14.7 % | 20.9 MB | 1.70 ms | 3.42 ms | 3 |
| 5 ms | 35.3 % | 21.3 MB | 2.20 ms | 5.20 ms | 3 |

At 100 ms that is what the Modbus path cost (4 % and 17 MB, 2026-10-24
morning) with per-channel quality added. A scan is ≈ 1.7 ms of waiting on
the processor's OptoMMP server whatever the scan rate, so 5 ms is the
floor: the slowest scan in that minute took 5.2 ms. 20 ms leaves the
processor's other tenants plenty.

### Streaming instead of polling

The project ships polling, because the other two modes write the
processor's own configuration. One line in `optommp_manifest.yaml`
changes that (or re-import with `--custom-area` / `--stream 20ms`):

- `custom-area: true` maps every word the scan needs into the processor's
  custom data area, so a scan is one read;
- `stream: 20ms` has the processor push that area to nautilus over UDP
  every 20 ms; nautilus then sends only writes, latch clears and a chassis
  check every 5 s.

Measured the same way on 2026-10-24 evening, with the processor's
`mmpServer` beside `naut` (it idles at 6.6 %):

| inputs | naut CPU | RSS | mmpServer CPU | per scan |
|---|---|---|---|---|
| packed poll, 20 ms | 13.7 % | 20.7 MB | 12.4 % | 3 requests, 1.62 ms |
| custom area, 20 ms | 12.0 % | 19.7 MB | 8.4 % | 1 request, 0.53 ms |
| stream, 20 ms | 10.0 % | 19.5 MB | 8.3 % | none; longest gap 25.7 ms |
| stream, 5 ms (task 5 ms) | 20.5 % | 21.5 MB | 11.1 % | none; longest gap 24.1 ms |

A clean stop (Ctrl-C, SIGTERM) puts the pointers and the streaming
configuration back as nautilus found them; a `kill -9` leaves the
processor streaming to a port nobody reads until nautilus next starts.
`watchdog: 500ms` sets the I/O unit's communication watchdog — outputs
given a watchdog value in groov Manage go to it after 500 ms without a
request; it is not set on this bench.

That run also found a runtime bug: the EPIC reports every **unwired
ICTD channel as NaN**, and one NaN tag made `/api/state` answer 200 with
an empty body and the SSE stream skip every frame — a blank dashboard
with no error anywhere. Fixed on this branch: a REAL that is not a
number renders as JSON `null`, the drivers mark such a tag's quality
`bad` (the link is fine, the reading is not), and the state
endpoint answers 500 with the encoder's reason instead of silence if
anything else ever fails to render.

The processor's PAC Control engine on 22001, groov View and Node-RED
are not touched. (There is no Ignition Edge on the bench unit.)
