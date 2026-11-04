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
  - {id: SIM,  type: analyser,        host: 127.0.0.1, port: 5020, unit-id: 4, desc: the bench simulator}
```

A device type may set `port:` (default 502) and an instance overrides it; an
instance's `host:` may also carry the port (`host: 127.0.0.1:5020`), which
the import splits.

Two loops on one host with different unit-ids is the gateway shape — an
Anybus or a Moxa fronting several serial devices is one source per drop,
same host. A plain TCP device is just an instance whose unit-id nobody
cares about.

```sh
naut modbus import --map devices.yaml
```

That emits **two committed, never-hand-edited files**, byte-identical on
every re-run so a regeneration diffs cleanly:

- `modbus_manifest.yaml` — `sources:` (`host`, `port` (default 502; the host alone, never `host:port`), `unitid`, `wordorder`,
  `timeout`, `enable`, `maxblock`) and `tags:` (source, table, address,
  format, scale, `writable`/`rewrite`/`writeonly`, `scanclass`). Decoded
  strictly — a misspelled key is an error with a did-you-mean, not a
  silently dropped binding.
- `tags/modbus.yaml` — an ordinary tag file (`role: input|output`,
  `type:`, unit, desc, init), composed with `tag-files: [tags/modbus.yaml]`.
  The type comes from the register format, so a program reads the tag
  without declaring it in `VAR_EXTERNAL`:

  | format | `type:` |
  | --- | --- |
  | `int16`, `uint16`, `int32`, `uint32` | `INT`, `UINT`, `DINT`, `UDINT` |
  | any of those with `scale:` or `offset:` | `REAL` |
  | `float32`, `float64` | `REAL`, `LREAL` |
  | a coil, a discrete input, `bool`, `bit:N` | `BOOL` |

The regeneration command lives in each generated file's header, so nobody
has to remember it. Useful flags: `--instance` for one instance id,
`--tags` / `--writable` globs to select and mark, `--tags-skip` to leave
tags the project declares by hand out of the tag file, `--out` and
`--tags-out` for paths, `--plan` to print the block plan.

`naut modbus tags --map devices.yaml modbus_manifest.yaml` re-derives
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
while the device answers. It is present from `Start` — before any poll
has landed — so interlock logic can gate on it safely:

```
GasAlarm := GAS__Online AND GAS_CH1 > 250.0;
```

not `GasAlarm := GAS_CH1 > 250.0` alone. Data tags stay absent from the
driver's snapshot until their block has been read at least once. In the
tag store they read as their `init:`, or the zero of their type, until
then: a source that is down when the controller starts does not fault the
programs that read its tags. Their quality is `NotConnected`
meanwhile. See [Inputs from a device that is
down](/guides/tag-model/#inputs-from-a-device-that-is-down).

What "answers" means:

- `__Online` starts false and goes true on the first answer on a
  connection, not when the TCP connection opens. A gateway that accepts
  the connection and never replies is never online.
- An answer is a reply, or any exception except the two gateway ones. An
  exception such as 0x02 (illegal data address) is the device answering
  about one span: the source stays online and only that block's tags go
  `Bad`.
- Exceptions 0x0A (gateway path unavailable) and 0x0B (gateway target
  device failed to respond) mean the device behind the gateway did not
  answer. The source goes offline (`__Online` false, its tags `Stale`),
  while the connection to the gateway stays up, once every block's latest
  read got one and the last three requests in a row did. One or two
  misses (a single RS-485 timeout or CRC error behind the gateway) do not
  count. The next answer brings it back.
- With blocks in different scan classes, a slow block whose last read
  answered keeps the source online until it is read again.
- On a polled source only reads decide. A write's reply does not bring
  the source online: it may come from the gateway itself, or from a
  keep-alive the gateway confirms while the device's reads get 0x0B.
- A write-only source (nothing polled) goes by its writes. With a
  `rewrite:` keep-alive it starts offline and goes online when the first
  re-assert is confirmed. Without one there is nothing to ask the device
  until the program commands it, so it is online as soon as the
  connection opens; a program that gates its commands on `__Online` can
  still send the first one. Either way, three gateway no-answers to its
  writes in a row take it offline, and a confirmed write brings it back.
  Without `rewrite:`, nothing else asks the device, so a program that
  sends commands only while `__Online` is true never sends the write that
  would bring it back: keep commanding while offline, or add `rewrite:`.
- A lost connection, or a source parked with `enable:`, is offline. A
  source with nothing due for over a second (nothing polled and no
  `rewrite:`, or a slow scan class) checks its socket once a second
  without sending a request, and checks it again before sending on a
  link that has been quiet for 100 ms or more. A reset connection is
  noticed within about a second. A link cut silently (a pulled cable, a
  dead switch) is caught by TCP keepalive, which the driver sets on
  every connection: an idle link is declared dead within about 15 s.
- A device that closes a connection cleanly (a FIN) while it is idle,
  as many gateways do on an idle timeout, is not an outage, provided it
  had kept the connection at least 100 ms. The driver reconnects at once,
  with no backoff, and the source stays online with nothing counted;
  the poll schedule carries on as before. Only if that reconnect fails
  does the source go offline and count one outage. A device that hangs
  up sooner than 100 ms after the connect (a crash-looping server, a
  gateway out of connection slots) is an outage each time, with backoff;
  every connection is checked once it is 100 ms old, so this is seen
  promptly. A device whose idle timeout resets the connection (an RST)
  instead of closing it counts an outage each time too.

This is the meaning `__Online` has on the other drivers: true when the
device is answering, not when a socket is open.

A source may also name an `enable:` tag — any BOOL the project already
declares. While it is false the source is **parked**: no connection is
attempted, its tags hold their last values and report `Stale`
(`NotConnected` if they were never delivered). Setting it true wakes the
source immediately. The enable tag is a command to the *driver*, not a
register write: it acts even while the write gate is closed, and it appears
in `OutputNames()` alongside the writable bindings.

Each source also gets `<source>__Outages`, a DINT input: the number of
times `__Online` has fallen from true to false since nautilus started.
Since `__Online` goes true only on an answer, the first connect is not an
outage, a source that has not answered yet has had none, and a gateway
that accepts the connection and never replies does not count, however
often the driver reconnects. A gateway whose field device stops
answering (0x0A or 0x0B on every block, three requests in a row) counts
one. A write-only source without `rewrite:` is online from the connect,
so losing its connection counts, even while it sends nothing; a device
closing it cleanly on an idle timeout does not, as long as it takes the
reconnect. Stopping nautilus
does not count. Parking a source with `enable:` counts, because
`__Online` goes false.
The counter only goes up, so a program that compares it with last scan's
value never misses an outage between scans. A program uses it to hold its
outputs after an outage until an operator acknowledges it; see [Holding
outputs until an operator
acknowledges](/guides/groov-epic/#holding-outputs-until-an-operator-acknowledges).
Like `__Online`, it is present from `Start`.

## Per-tag quality

Modbus has two entirely different kinds of failure, and the driver treats
them differently on purpose.

**An exception response** — the device answered and refused: illegal data
address, illegal function, server busy. The connection
is fine and *this span of addresses* is wrong. That block's tags go `Bad`,
its siblings on the same device stay Good, and the driver keeps polling —
it does not reconnect over a bad register map. Three consecutive exceptions
park just that block for one `retrymin` period so a permanently illegal
address stops burning a request slot every cycle; it retries after that.
The refusal surfaces as `badBlocks` on the device row. The gateway
exceptions 0x0A and 0x0B are handled the same way per block, but they say
the device did not answer at all: when every block gets one, the source
goes offline and its tags `Stale` (see the `__Online` companion above).

**A transport failure** — dial refused, timeout, a short read, a response
carrying the wrong unit id, an idle socket that was reset or that TCP
keepalive gave up on, or a device that closed an idle connection and
then refused the reconnect. The whole source is down: the driver drops
the connection and reconnects with exponential backoff from `retrymin`
(1s) to `retrymax` (60s). The ladder starts over once the device has
answered on a connection, not when the dial succeeds, so a device that
accepts connections and never answers keeps climbing it. An idle
connection the device closes cleanly is not a failure: see the
`__Online` rules above. **Values hold.** The driver zeroes
and invents nothing; a tag delivered at least once reports `Stale` with
its age, a tag never delivered reports `NotConnected` (the tag store holds
its `init:` or zero for the logic meanwhile), and `<source>__Online` goes
false.

Quality is reported through `io.QualityReporter`, non-Good entries only —
a healthy plant reports an empty map. Quality is **source-scoped**: one
dead device takes exactly its own tags stale, never the plant's. The
`__Online` and `__Outages` companions are the driver's own truth and are
always Good.

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
counted as `queuedWrites` on the device row. If the link breaks partway
through a flush, the write that broke and the ones after it are queued
again. Writes the device confirmed or refused with an exception earlier in
that flush are not re-sent, so a value another master has written to those
registers since is left alone. Not so for a `rewrite:` binding: it
re-asserts its last commanded value on connect and on every period, so it
overwrites the other master's value anyway.

**An exception to a write depends on whether the write ran.** When the
device itself refuses a write (0x02 illegal data address, 0x03 illegal
data value, or any exception other than the three below), the write is
dropped and the same value is not sent again (until the write gate
reopens, see [Redundancy](#redundancy)). Sending it again would only be
refused again, so only a changed value is a new command. 0x04 (server
device failure) is handled the same way: the write may have partly run,
and on a register that triggers an action a second write could fire it
twice. When a gateway answers 0x0A (path unavailable) or 0x0B (target
device failed to respond), the device behind it never got the command, and
when the device answers 0x06 (server device busy), it did not carry the
command out. Such a write is dropped from the queue and held back for the
source's `retrymin` (1 s by default, the same pause a block's polls get
after repeated exceptions). Then the driver forgets it and asks the
runtime for the outputs, so the next scan hands them over and the
program's value goes out again as a command even if it has not changed.
That is the program's current value, never a replay of the one that
failed. A write that keeps not running is therefore tried once per
`retrymin`, not once per scan, so a busy device or an RS-485 line behind a
gateway is not flooded. A changed value is a new command and goes out at
once, backoff or not. Only the tags whose write did not run are forgotten:
on a `bit:N` register, the bits whose read-modify-write got the exception
(on its read or on its write), not the register's other bits. A `rewrite:`
binding is not forgotten, because its next re-assert sends it again
anyway. While the write gate is closed nothing is sent, and the forgotten
command goes out with every other output once the gate reopens (see
[Redundancy](#redundancy)). Every exception to a write counts in
`exceptions` on the device row. The log says `write not delivered; will
resend` once when a write first fails to run (not on every attempt) and
`write delivered after it did not run`, at Info with the number of
attempts, when it lands; it says `write refused` for the rest. On a
write-only source, a gateway no-answer to a write also counts toward
taking it offline, as described above; 0x06 is the device answering.

**`rewrite:` is the exception, and it is a keep-alive.** Some devices watch
a command register and fail safe if it stops moving — a safety controller's
enable word, a VFD control word, an IO-Link master's Modbus watchdog. A
binding with `rewrite: 2.5s` re-asserts its last commanded value on that
period even when nothing changed, and its baseline value *is* written on
connect, because a watchdog that never sees a first write drops out
immediately. After the quiet reconnect that follows a device closing an
idle connection, nothing is written at once: the value is re-asserted
when its period next falls due, as it would have been on the old
connection.

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
lone register or coil goes as FC6 or FC5. A source's changed values from
one scan are queued together, so a flush takes all of them or none: a run
changed in one scan is never split across two frames by a poll that
happened mid-scan. Requests on a source are strictly
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
`127.0.0.1:5020`. Point an instance's `host:` at it (`host: 127.0.0.1:5020`,
or `host: 127.0.0.1` with `port: 5020`; the import splits the former into
the manifest's `host:` and `port:`) and `naut run .`
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
merges per tag, and on a `sparkplug:` edge node each Modbus source and each
other driver is its own Sparkplug device, born and killed by its own
connection — one dead bus takes only its own tags offline (see
[Sparkplug B](/guides/sparkplug/#devices-and-connection-health)). The
`memory` loopback cannot join a list: it owns whatever is written to it,
which is exactly what makes it unroutable next to another driver.

## Redundancy

With a `redundancy:` section, the driver's write gate is wired to leadership
— commands leave the driver only on the leader, and a `SetWriteGate` func
can compose a "logic healthy" signal into the same condition. Leadership
is checked again when queued commands go out: a replica that lost it
during an outage drops what it queued, and so do the writes a broken
flush put back. It is checked again before each write of a flush, so a
long flush through a slow gateway stops sending once the gate closes.

**When the gate reopens, every output goes out again.** While it was
closed another replica may have led and written the same registers, so
nothing this replica sent before is trusted to still be there. On the
first scan after the driver sees the gate reopen, every output is sent
once with the program's current value, including one that has not changed
since this replica last led. A replica that regains leadership pushes
because it took over; a gate that reopens without a takeover (logic
healthy again) pushes because the driver asks the runtime for the outputs
when it sees the gate reopen. Commands the gate dropped go out in the same
push. Until that push, a `rewrite:` binding re-asserts nothing, so the
value from the previous term is never sent: the keep-alive restarts with
the program's value.

**A replica that has been a standby has no baseline.** Once the driver has
seen the write gate closed, the start-up baseline is off: a replica that
starts as a standby commands every output with its first push as leader,
as the new primary of a hot-standby PLC pair drives its outputs at once. A
plain start, leader from the first scan and never gated, keeps the
baseline: `naut run` starts the driver only after the redundancy elector
has made its first decision, so a replica that acquires the lease at once
never sees the gate closed. A driver restarted after it saw the gate
closed is still a standby. A standby's reconnect re-asserts no `rewrite:`
binding.

**A write-to-trigger register is written again too.** The re-send does not
know what a register does. One whose every write starts something (a
recipe number, a step command, a reset) is written again on every regain
of leadership and every reopen of the gate, with the program's current
value, even when that value has not changed, and even when its last write
was refused with 0x04. Gate such a command in the program, or make it
edge-like (write it, then a neutral value), if a second write would act
twice.

**A flip between two looks is not seen.** A gate func cannot say it
changed, so the driver looks at it: on every push, flush and re-assert, on
every wake of a connected source's loop (at least once a second), on every
connect attempt, and before the on-connect re-assert. A gate that closes
and reopens between two looks is not seen, and then nothing above happens.
The window is about a second while a source is connected, up to `retrymax`
(60 s by default) while every source is down, and unbounded while every
source is parked.

**A closed gate stops the `rewrite:` re-asserts too, by design.** A device
whose keep-alive word goes quiet trips its own fail-safe, which is exactly
what must happen when this replica is not the leader or its logic is
faulted. A hung controller that kept refreshing a stale command would mask
the fault instead, leaving the device happily executing a setpoint nobody
computes any more. Reads are unaffected on both replicas.

**A hung scan closes the gate, with or without `redundancy:`.** `naut run`
gates the driver on live scans in every project. When one scan has been in
progress for longer than its bound (twice its task's period, 1 to 2 s), as
with logic that never returns or a driver call that blocks, the gate closes
about 4 s later, 5 to 8 s into the hang. The `rewrite:` re-asserts stop and
the device's watchdog trips. When scans complete again, the gate reopens
and every output goes out once. Without `redundancy:`, a healthy project
sees no difference: the gate is open from the start (see
[Redundancy](/guides/redundancy/#redundancy)).

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
