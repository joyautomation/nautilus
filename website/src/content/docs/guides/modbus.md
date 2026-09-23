---
title: Modbus TCP
description: Poll field devices over Modbus TCP from a committed device map — block reads, per-source word order, per-tag quality, keep-alive rewrites, and a bench slave that needs no hardware.
---

A `driver: {type: modbus}` section puts a manifest project on the oldest
field bus that is still everywhere: PID loops behind a gateway, VFDs, gas
analysers, power meters, IO-Link masters. Everything the driver polls comes
from one hand-written **device map** — register maps off the datasheet once
per device *type*, hosts and unit-ids once per *instance* — which
`naut modbus import` turns into a committed manifest and a committed tag
file. Shape: a manifest-tier `io.Driver`, mirroring `naut eip import`
end to end.

```yaml
driver:
  type: modbus
  manifest: modbus_manifest.yaml   # from `naut modbus import`
  scan-rate: 1s                    # the default scan class
  scan-classes: { fast: 500ms, slow: 2.5s }
  tag-classes:
    fast: ["FAN_*"]
    slow: ["*_Totals"]
tag-files: [tags/modbus.yaml]
```

`manifest:` is the only required key — a project missing it fails
`naut check`, not `naut run`, so a bad config never reaches the
field. `New` **never dials**: it decodes the manifest, validates every
cross-reference and format, and computes the block-read plan offline, so
`naut check` and `naut build` pass in CI with no device in sight.
The connection is `Start`'s job.

## Generating the manifest: `naut modbus import`

The one file you write by hand is the device map. It describes device
**types** once — the register map, formats, scaling, word order, straight
off the datasheet — and **instances** per physical device or gateway drop —
host, unit-id, overrides:

```yaml
# devices.yaml
devices:
  temp-controller:
    description: PID temperature controller behind a Modbus TCP gateway
    scan-class: slow
    registers:
      - {tag: Tpv, address: 0, format: int32, scale: 0.1, unit: degC, desc: process temperature}
      - {tag: Tsp, address: 262, format: int32, scale: 0.1, writable: true, rewrite: 2.5s, init: 0.0, unit: degC, desc: temperature setpoint}
      - {tag: AlmH, address: 12, table: discrete, desc: high temperature alarm}
  analyser:
    word-order: little
    timeout: 2s
    max-block: 64
    registers:
      - {tag: CH1, address: 0, format: float32, unit: ppm, desc: channel 1}
      - {tag: CH2, address: 2, format: float32, unit: ppm, desc: channel 2}
instances:
  - {id: TC_A, type: temp-controller, host: 192.168.10.10, unit-id: 1, enable-tag: CFG_HeatersOn, desc: zone A loop}
  - {id: TC_B, type: temp-controller, host: 192.168.10.10, unit-id: 2, enable-tag: CFG_HeatersOn, desc: zone B loop}
  - {id: GAS,  type: analyser,        host: 192.168.10.51, unit-id: 4, desc: stack analyser}
```

Two loops on one host with different unit-ids is the gateway shape — an
Anybus or a Moxa fronting several serial devices is one source per drop,
same host. A plain TCP device is just an instance whose unit-id nobody
cares about.

```sh
naut modbus import --map devices.yaml
```

That emits **two committed, never-hand-edited files**, byte-identical on
every re-run so a regeneration diffs cleanly:

- `modbus_manifest.yaml` — `sources:` (host, `unitid`, `wordorder`,
  `timeout`, `enable`, `maxblock`) and `tags:` (source, table, address,
  format, scale, `writable`/`rewrite`/`writeonly`, `scanclass`). Decoded
  strictly — a misspelled key is an error with a did-you-mean, not a
  silently dropped binding.
- `tags/modbus.yaml` — an ordinary tag file (`role: input|output`, unit,
  desc, init), composed with `tag-files: [tags/modbus.yaml]`.

The regeneration command lives in each generated file's header, so nobody
has to remember it. Useful flags: `--instance` for one instance id,
`--tags` / `--writable` globs to select and mark, `--tags-skip` to leave
tags the project declares by hand out of the tag file, `--out` and
`--tags-out` for paths, `--plan` to print the block plan.

`naut modbus tags modbus_manifest.yaml --map devices.yaml` re-derives
just the tag file from an already-committed manifest. Without `--map` it
still works; the unit/desc/init columns, which only the map knows, are
dropped.

## The block plan

tentacle-era drivers issued one Modbus request per variable per interval. A
sixteen-register analyser then costs sixteen round trips a second. The
planner coalesces every binding on the same (source, table, scan class)
into as few block reads as the protocol allows:

```sh
naut modbus import --map devices.yaml --plan
```

```
source GAS: 2 blocks
  FC3 holding 0..7 (8 registers, class default): GAS_CH1@0, GAS_CH2@2, GAS_CH3@4, GAS_CH4@6
  FC4 input 0..1 (2 registers, class default): GAS_Flow@0
source TC_A: 3 blocks
  FC3 holding 0..9 (10 registers, class slow): TC_A_Tpv@0, TC_A_MV@8
  FC3 holding 262..263 (2 registers, class slow): TC_A_Tsp@262
  FC2 discrete 12..12 (1 coils, class slow): TC_A_AlmH@12
```

Four float32 channels become **one** FC3 request. Every tag prints with its
address so a commissioning tech can check it against the datasheet.

Three knobs shape the plan:

- **Gap.** A block may span up to 8 unaddressed registers rather than pay
  for a second round trip — reading eight unused words is cheaper than
  another request. `WithBlockGap(0)` never bridges a hole, the escape hatch
  for devices that fault a read touching an unimplemented register.
- **`max-block`.** Caps one read for devices that reject long ones (some
  ADAM firmware stops at 64). Absent, the protocol maxima apply: 125
  registers per FC3/FC4, 2000 coils per FC1/FC2.
- **Scan classes.** A binding's `scan-class` names its poll group;
  unassigned bindings are the `default` class, whose rate is `scan-rate`.
  The reserved class **`none`** catalogs a tag without ever polling it — a
  command register the program only writes belongs there. `tag-classes:`
  globs override the generated class from `nautilus.yaml`, so re-running
  the import never erases polling policy.

## Addressing, word order, byte order

**Addresses are 0-based PDU addresses.** What a datasheet calls **40001 is
holding address 0**; 30001 is input address 0. No option flips this — see
*Not supported*. `naut modbus browse --from` takes the same 0-based
address, so the number you type there is the number you put in the map.

Tables are `holding`, `input`, `coil`, `discrete`. Holding and coil are
writable; input and discrete are read-only in the protocol itself, and
marking one `writable` is a validation error.

Formats: `int16`, `uint16`, `int32`, `uint32`, `float32`, `float64`,
`bool`, and `bit:N` for one bit (0..15) of a register. Bit tables take only
`bool`. Scaling is `engineering = raw * scale + offset`, applied on read
and inverted on write; `scale: 0` means 1, so an unset field is the
identity rather than a zeroed tag.

Word and byte order are **per source**, not global, because the same
hardware ships both ways:

```yaml
sources:
  - id: GAS
    host: 192.168.10.51
    unitid: 4
    wordorder: little   # big (default) | little — register order of a multi-register value
    byteorder: big      # big (default) | little — byte order inside each register
```

`wordorder` reverses the register sequence of a 32- or 64-bit value;
`byteorder` swaps the two bytes inside each register. If a float32 reads as
garbage whose magnitude still looks plausible, flip `wordorder` first —
`naut modbus browse` settles it in seconds by printing every candidate
format side by side.

## The `__Online` companion and `enable:`

Each source gets a driver-synthesized `<source>__Online` BOOL input, true
while its connection is up. It is present from `Start` — before any poll
has landed — so interlock logic can gate on it safely:

```
GasAlarm := GAS__Online AND GAS_CH1 > 250.0;
```

not `GasAlarm := GAS_CH1 > 250.0` alone. Data tags stay absent from the
snapshot until their block has been read at least once.

A source may also name an `enable:` tag — any BOOL the project already
declares. While it is false the source is **parked**: no connection is
attempted, its tags hold their last values and report `Stale`
(`NotConnected` if they were never delivered). Setting it true wakes the
source immediately. The enable tag is a command to the *driver*, not a
register write: it acts even while the write gate is closed, and it appears
in `OutputNames()` alongside the writable bindings.

## Per-tag quality

Modbus has two entirely different kinds of failure, and the driver treats
them differently on purpose.

**An exception response** — the device answered and refused: illegal data
address, illegal function, gateway target failed to respond. The connection
is fine and *this span of addresses* is wrong. That block's tags go `Bad`,
its siblings on the same device stay Good, and the driver keeps polling —
it does not reconnect over a bad register map. Three consecutive exceptions
park just that block for one `retrymin` period so a permanently illegal
address stops burning a request slot every cycle; it retries after that.
The refusal surfaces as `badBlocks` on the device row.

**A transport failure** — dial refused, timeout, a short read, a response
carrying the wrong unit id. The whole source is down: the driver drops the
connection and reconnects with exponential backoff from `retrymin` (1s) to
`retrymax` (60s), resetting on success. **Values hold.** Nothing is zeroed
and nothing is invented; a tag delivered at least once reports `Stale` with
its age, a tag never delivered reports `NotConnected`, and `<source>__Online`
goes false.

Quality is reported through `io.QualityReporter`, non-Good entries only —
a healthy plant reports an empty map. Quality is **source-scoped**: one
dead device takes exactly its own tags stale, never the plant's. The
`__Online` companions are the driver's own truth and are always Good.

## Writes

A `writable: true` binding on a holding or coil table becomes a `role:
output` tag whose writes reach the device as FC 5/6/15/16.

**Outputs are commands; an unchanged output since start is not a command.**
An output's value before anyone writes it is its `init:` — usually zero —
and taken at face value, a controller restarting against a running plant
would command every setpoint to zero. So the **first snapshot after `Start`
is the baseline**: recorded, sent to nobody. Thereafter only a value that
actually moved goes on the wire, once. A command raised while the source is
down is queued per source (latest value per tag) and flushed on reconnect,
counted as `queuedWrites` on the device row.

**`rewrite:` is the exception, and it is a keep-alive.** Some devices watch
a command register and fail safe if it stops moving — a safety controller's
enable word, a VFD control word, an IO-Link master's Modbus watchdog. A
binding with `rewrite: 2.5s` re-asserts its last commanded value on that
period even when nothing changed, and its baseline value *is* written on
connect, because a watchdog that never sees a first write drops out
immediately.

```yaml
- {tag: Tsp, address: 262, format: int32, scale: 0.1, writable: true, rewrite: 2.5s, init: 0.0}
- {tag: Control, address: 2100, writable: true, write-only: true, init: 0.0}
```

**`write-only`** takes a writable binding out of the read plan, for the rare
register that cannot be read back (some drive control words). Without it a
writable binding is *also* polled, and the read-back is the source of truth
after a restart — which is what you want almost always.

Writes are **coalesced**: exactly adjacent register writes staged in one
flush merge into a single FC16 run, adjacent coils into one FC15, and a
lone register or coil goes as FC6 or FC5. Requests on a source are strictly
sequential — Modbus TCP permits pipelining by transaction id, but the
gateways in the field do not, so one outstanding request is the default.

## `/api/drivers`

`Kind: "modbus"`, with **one device row per source** — `{ID: "TC_A",
Online: true, Detail: "3 blocks"}`, or `"3 blocks · 1 refused"` when a block
is exception-marked. The driver state climbs `connecting` → `error`
(nothing answers) → `degraded` (a source dark, or any block refused) →
`connected`, plus `waiting` when every source is parked by its enable tag.
Metrics: `sources` (`connected / total`), `blocks`, `reads`, `writes`,
`errors`, and `queued writes` when any command is parked.

`Extra["sources"]` carries the structured twin of the rows — `id`, `addr`,
`state` (`connected | connecting | error | parked`), `sinceMs`, `blocks`,
`badBlocks`, `queuedWrites`, `rttMs` (an EWMA of request round-trip),
`retries`, `exceptions`. That is the shape `DriverStatusPanel` and
`DriverStatusCard` already render for an `eip` driver, so a Modbus project's
comms status needs zero HMI changes.

## Bench without a device: `serve` and `browse`

The in-repo slave stands in for a whole plant on one listener, multi-unit
like a real gateway, built from the manifest alone:

```sh
naut modbus serve --manifest modbus_manifest.yaml --values seed.json --ramp
```

`--values` is a JSON `{tag: value}` file in **engineering units** — serve
inverts each binding's scaling and word order on the way into the registers,
so you write `"GAS_CH1": 120.0`, not a pair of raw words. `--ramp` drifts
the numeric inputs so trends look alive. `--listen` defaults to
`127.0.0.1:5020`. Point the manifest's hosts at it and `naut run .`
polls the same plan `--plan` printed.

`browse` is the commissioning poke — read a range from a live device (or
the bench) and see every address raw and decoded:

```sh
naut modbus browse --host 127.0.0.1 --port 5020 --unit 4 --from 0 --count 8
naut modbus browse --host 192.168.10.51 --unit 4 --word-order little --format float32 --count 8
```

Without `--format` it prints every format side by side, which is how you
find a device's real word order in one shot. Other flags: `--table`
(holding | input | coil | discrete), `--timeout`.

## Modbus alongside another bus: `drivers:`

A skid is rarely the whole plant. When the same controller also consumes a
Sparkplug fleet, or polls a Logix PLC over EtherNet/IP, list the drivers
instead of picking one — `drivers:` is the plural of `driver:`, each entry
the same shape:

```yaml
drivers:
  - type: modbus
    manifest: modbus_manifest.yaml
    scan-classes: { fast: 500ms }
  - type: sparkplug-host
    name: fleet                      # optional; default is the type, deduped as "eip-2"
    broker: "tcp://mqtt.plant:1883"
    group-id: Plant
    host-id: plant-scada
    manifest: sparkplug_manifest.yaml
tag-files: [tags/modbus.yaml, tags/sparkplug.yaml]
```

Reads fan out to every driver and merge; a write goes to the one driver
whose bindings claim the tag. Ownership is disjoint by construction: a tag
delivered by two drivers, or writable through two, is a **load error naming
both** — `naut check` reports it offline, the same no-last-wins rule
tag files keep. Setting `driver:` and `drivers:` together is an error too;
move the single driver into the list. On `/api/drivers` each driver keeps
its own row (the Modbus one still has a device row per source), quality
merges per tag, and a Sparkplug `device:` on the same controller is healthy
only when every child bus is — an operator reading the device online should
be able to trust all of its tags, not the subset whose bus is up. The
`memory` loopback cannot join a list: it owns whatever is written to it,
which is exactly what makes it unroutable next to another driver.

## Redundancy

With a `redundancy:` section, the driver's write gate is wired to leadership
— commands leave the driver only on the leader, and a `SetWriteGate` func
can compose a "logic healthy" signal into the same condition.

**A closed gate stops the `rewrite:` re-asserts too, by design.** A device
whose keep-alive word goes quiet trips its own fail-safe, which is exactly
what must happen when this replica is not the leader or its logic is
faulted. A hung controller that kept refreshing a stale command would mask
the fault instead, leaving the device happily executing a setpoint nobody
computes any more. Reads are unaffected on both replicas.

## Testing

`naut test` never opens a socket. Keep a `memory.yaml` beside
`nautilus.yaml` with the same tags and `driver: {type: memory}`, and the
acceptance suites run in virtual time against the loopback driver:

```sh
naut check -m memory.yaml .
naut test  -m memory.yaml .
```

`given:` writes the driver's input image exactly the way a block read
would, so the same `*_test.yaml` files pass unchanged once the real driver
is polling — a ten-second interlock or a PI loop's settling time asserted
deterministically, in milliseconds, on a laptop with nothing on the network.

The wire layer gets a second opinion: CI runs the driver against a
**pymodbus** server — a foreign implementation, not our own slave — seeded
with known raw words for every format in both word orders across all four
tables, plus an unimplemented range that answers exception 0x02. It asserts
that a foreign encoder agrees with ours, that each writable format round
trips, that the refused range marks its block bad while siblings stay Good,
and that a slow response drives the reconnect path with values held.

## Not supported

- **Modbus RTU and ASCII over serial.** TCP only. A serial device reaches
  nautilus through a TCP gateway, which is the deployed shape anyway — and
  the gateway's drops are one source each.
- **BCD, `int64`, and string formats.** The set is `int16`, `uint16`,
  `int32`, `uint32`, `float32`, `float64`, `bool`, `bit:N`.
- **One-based addressing.** There is no `--one-based` or `base: 40001`
  option, and there will not be: addresses are 0-based PDU addresses
  everywhere — map, manifest, `browse`, and the plan output — so there is
  exactly one convention to hold in your head. Subtract once, in the map.
- **Pipelined requests.** One outstanding request per source, because the
  gateways in the field do not pipeline reliably.

## From Go

The manifest section is the manifest form of the `modbus` package — a
custom topology, or polling policy computed rather than configured, uses it
directly as an `io.Driver`:

```go
import "github.com/joyautomation/nautilus/modbus"

m, err := modbus.ParseManifest(manifestYAML)
driver, err := modbus.New(m,
    modbus.WithScanRate(time.Second),                  // the default class
    modbus.WithScanClass("fast", 500*time.Millisecond),
    modbus.WithScanClass("slow", 10*time.Second),
    modbus.WithTagClass("fast", "FAN_*"),              // globs on tag names
    modbus.WithTagClass(modbus.NoPoll, "*_Control"),   // cataloged, never polled
    modbus.WithBlockGap(0),                            // never bridge a hole
    modbus.WithLogger(slog.Default().With("driver", "modbus")),
)
driver.Start(ctx)

rt, err := runtime.New(runtime.Options{
    Program: program,
    Driver:  driver,
    Inputs:  driver.InputNames(),
    Outputs: driver.OutputNames(),
})
```

`WithDialer` substitutes the TCP dial, which is how the tests hand the
driver an in-process slave or a recording wrapper that asserts what hits the
wire. `driver.Plan()` returns the computed block plan, `Health()` the
per-source rows behind `/api/drivers`, and `SetWriteGate` the redundancy
hook described above. `Stop` closes every source's connection; `New` never
opened one.
