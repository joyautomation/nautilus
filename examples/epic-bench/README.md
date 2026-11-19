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
naut optommp browse --host <epic>   # what the processor reports: modules, channel types, live values
naut optommp import --host <epic> --manifest-host 127.0.0.1 --scan-rate 20ms --scratch-floats 1 --watchdog 1s
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
naut run   .     # on the EPIC: dashboard + tag API on http://localhost:8086
```

The manifest dials `127.0.0.1`, as the project runs on the processor
([On the EPIC itself](#on-the-epic-itself)). To run it from a laptop
against the EPIC over the network instead, point `host:` at the EPIC,
on your copy only:

```sh
sed -i 's/^host: 127.0.0.1$/host: <epic>/' optommp_manifest.yaml   # <epic>: the processor's address
naut run .
git checkout optommp_manifest.yaml                                  # back to loopback before you commit
```

or re-import with `--host <epic>` and no `--manifest-host`, which
writes the address into both generated files. Either way the dashboard
is on the laptop, and the program and its outputs then depend on the
network between the two.

## Without the EPIC

`sim.yaml` is the same program with the `memory` driver: the chassis's
inputs are tags you write, with the import's names (`tags/sim.yaml`).
The four field wires on the bench, DO3→DI6, DO4→DI8, DO5→DI10 and
DO6→DI12, are `board.st`, so `EPIC_M0_DI03` on lights DO3, then DI6, DO6
and DI12, as on the desk. `EPIC_M2_AI00` is the ICTD probe (°C),
`EPIC_M3_AI00` the voltage input, and `EPIC__Online` false is an outage.

**Unwired channels differ.** Only ICTD channel 0 is wired on the bench;
the other eleven read NaN there, so their tags are Bad and the 3D screen
shows them as unwired. A simulated tag starts at its `init:` and a tag
file cannot spell NaN, so here those eleven read 0 °C with good
quality. Don't read a 0 on them as a measurement, and write a value to
one if a test needs it.

```sh
naut test -m sim.yaml .     # the same suite
naut run  -m sim.yaml .     # dashboard + tag API + the 3D screen on http://localhost:8086
```

Nothing about OptoMMP or its timing is simulated. The nautilus
playground runs this build, its 3D screen included.

`bench.st` mirrors the first eleven inputs to the first eleven outputs,
puts a 1 Hz heartbeat on output 11 (`BlinkPeriodS` is an operator
setpoint), copies the two wired analog channels onto `BenchTempC` and
`BenchVoltsV`, and publishes the temperature to scratchpad float 0.
With the `HoldAfterOutage` setpoint on (it starts off, so the bench
behaves as before), after an outage (the EPIC unreachable, the stream
late, or its communication watchdog possibly tripped) the lamps stay
dark until `BenchAck` is written: the driver's status tags `EPIC__Online`,
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

## The 3D screen

`hmi/` is a small SvelteKit page that draws the bench unit as it stands
on the desk: the GRV-EPIC-PR1 with its screen on, the four modules in
slot order on the GRV-EPIC-CHS4, the clear acrylic stand it leans back
on, and the small bar beside it: the load panel's two push buttons and
its knob. On the simulated build (`sim.yaml`, the playground) the bar
works. The top button latches (DI0; a click turns it on or off) and lights
while it is in. The bottom one is momentary (DI1; hold to press) and its
blue LED ring is DO0, an output the program drives. `bench.st` mirrors
DI0 to DO0, so latching the top button lights the bottom one's ring.
Drag around the knob to turn it: the panel's potentiometer, on the
voltage input slot 3 channel 0 (0 to 10 V here). The wiring is in
`hmi/src/lib/bar.ts`, from the I/O mapping of this unit's load panel.
On the processor the bar stays as drawn and the screen writes nothing. The bar sits to the right of the chassis; orbit the view to
reach it. The bodies are the four small models in `hmi/static/models`,
simplified from Opto 22's published CAD (its README says how); the
stand, the bar and everything live are drawn by the page. Each discrete channel's lamp is its number on the cover,
which glows white when the channel is on: the 24 inputs, and the 12
outputs with the heartbeat on 11. The analog modules' covers are blank,
as on the unit; their readings sit in a tag over each module (bad
quality reads "unwired", never zero). Each module's status light has the
colour it has on the bench (green, green, orange, blue) and goes dark
when `EPIC__Online` is false. A side panel has the driver's status and
the scan times. Hovering a channel names its tag. It only reads: one
`/api/stream`, held only while the tab is showing (a browser allows six
connections to a controller across all its tabs), nothing written.

```sh
cd hmi
npm install
npm run build                                          # → hmi/build, which server.hmi serves at /
CONTROLLER_URL=http://<epic>:8086 npm run dev  # or work on it against a running controller
```

The model is plain Threlte (`hmi/src/lib/Epic.svelte`) placed inside
`SceneView` from `@joyautomation/nautilus-hmi-3d`, which supplies the
canvas, the orbit camera and the tags. It is not a `*.scene.json`, so
`naut check` does not hold its tag names to the project; they are the
import's names, built from the module table at the top of that file. The
camera refits when the window changes shape, and the panel moves under
the model in a square or tall window; a tall window closes in on the
processor and the modules.

## On the EPIC itself

The same project runs *on* the processor. The EPIC is a quad-core ARMv7
(Cortex-A9 class, VFPv3/NEON) running Opto's Yocto Linux 4.1.3 on a
PREEMPT_RT 5.15 kernel with 1.8 GB RAM and 4 GB free on `/`, so the
static ARM build of `naut` runs unchanged:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags='-s -w' -o naut-arm ./cmd/naut
scp naut-arm <shell-user>@<epic>:nautilus/naut
scp -r examples/epic-bench <shell-user>@<epic>:nautilus/
ssh <shell-user>@<epic> 'cd nautilus/epic-bench && ../naut run .'
```

Shell access needs Opto 22's free **GROOV-LIC-SHELL** license (groov
Manage → System → License) and then a shell user created and the shell
server started on the Shell Access page — the shell user is its own
account, not the groov Manage admin. The shell user has sudo with a
password; nothing here needs it.

Verified 2026-10-24 on the bench, `naut run` on the EPIC with the
OptoMMP driver dialing the processor's own LAN address (the committed
manifest now dials loopback, `--manifest-host 127.0.0.1`, which costs
the same per request), the EPIC's own stack (PAC Control engine,
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
`watchdog: 1s` in the manifest (the import's `--watchdog 1s`) sets the
I/O unit's communication watchdog. An output that has a watchdog value
in groov Manage goes to that value after 1 s without a request. On this
bench DO7's watchdog value is on and DO11's is off. Set it on every
project: it is what puts the outputs in a safe state whenever nautilus
is not writing them. That covers a stop, a crash, a reboot, and a scan
that hangs. In the hung-scan case, nautilus drops its session once the
scans stop completing, so the unit's watchdog acts (measured on this
bench: DO7 at its watchdog value about 6 s into a hang). Measurements
are in the [groov EPIC guide](https://nautilus.joyautomation.com/guides/groov-epic/).
Give each output its watchdog value in groov Manage. With none, the
output keeps its last state.

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
