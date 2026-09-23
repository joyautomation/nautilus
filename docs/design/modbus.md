# Design brief: Modbus TCP driver for nautilus

Status: **built** on the `modbus` branch — `modbus/` (wire, encode, plan,
driver, status, `slave/`, `codegen/`), `naut modbus import|browse|serve|tags`,
project wiring and schema, `examples/modbus`. Verified against its own
in-process slave and, as a foreign implementation, against a pymodbus server
run as a CI job (§7.5). A run against real hardware is still pending; §9's
device quirks are argued, not yet measured.

**Goal:** a **manifest-tier Modbus TCP driver** (`driver: {type: modbus}`) plus
`naut modbus import|browse|serve`, mirroring `eip` and `sparkplug-host` end
to end. Modbus TCP :502 is what most of the field still speaks — PID
controllers, VFDs, analysers, remote I/O, protocol gateways — so it is the
second-most valuable bus after EtherNet/IP, and the first one a project is
likely to need *alongside* another. Hence two pieces: **A. `modbus/`**, the
driver (the bulk), and **B. `drivers:`**, more than one driver per project
(today `driver:` is a single object in `internal/project/project.go` — small,
but it touches the one struct everyone touches).

## 1. Package layout & public API

```
modbus/
  tcp.go        MBAP framing + FC 1/2/3/4/5/6/15/16 over net.Conn — pure stdlib, no dependency
  encode.go     register ⇄ ir.Value: int16/uint16/int32/uint32/float32/float64/bool/bit:n,
                word order, byte order, scale/offset
  manifest.go   Manifest{Sources, Tags}, YAML shape shared with the generator
  plan.go       bindings → block-read plan per (source, table, scan class), ≤125 regs / ≤2000 coils
  driver.go     io.Driver + io.BatchReader + io.QualityReporter; one goroutine per source
  status.go     Status → server.DriverStatus (one device row per source; VolatileExtra)
  codegen/      `naut modbus import` → modbus_manifest.yaml + tags/modbus.yaml
  slave/        in-repo Modbus TCP server (tests; `naut modbus serve` bench)
```

```go
package modbus

type Manifest struct {
	Sources []Source
	Tags    []TagBinding
}
type Source struct {
	ID        string        // "FTIR_I" — the device row on /api/drivers, and the quality scope
	Host      string        // "192.0.2.51"
	Port      int           // 502
	UnitID    uint8         // 1; protocol gateways use one unit-id per device behind them
	WordOrder string        // "big" (default) | "little"  — tentacle's reverseWords
	ByteOrder string        // "big" (default) | "little"  — tentacle's reverseBits
	Timeout   time.Duration // 3s
	RetryMin  time.Duration // 1s   backoff floor
	RetryMax  time.Duration // 60s  backoff ceiling
	Enable    string        // optional BOOL tag; false = source parked
	MaxBlock  int           // per-source cap on registers per read block (0 = protocol max; §3)
}
type TagBinding struct {
	Name      string  // nautilus tag (VAR_EXTERNAL name)
	Source    string  // Source.ID
	Table     string  // holding | input | coil | discrete
	Address   uint16  // 0-based PDU address
	Format    string  // int16 uint16 int32 uint32 float32 float64 bool bit:N
	Scale     float64 // engineering = raw*Scale + Offset  (0 = 1)
	Offset    float64
	Writable  bool    // output tag: runtime writes propagate (FC 5/6/16)
	Rewrite   time.Duration // >0: re-assert the last output every Rewrite even if unchanged
	ScanClass string  // poll group; "" = default; NoPoll = catalog only
}

type Option func(*Driver)
func WithScanRate(time.Duration) Option
func WithScanClass(name string, rate time.Duration) Option
func WithTagClass(class string, patterns ...string) Option   // same override rule as eip
func WithLogger(*slog.Logger) Option
func WithDialer(func(ctx context.Context, addr string) (net.Conn, error)) Option // tests

func New(m Manifest, opts ...Option) (*Driver, error)   // NEVER dials; validates the plan
func (d *Driver) Start(ctx context.Context)             // runcmd.go asserts this
func (d *Driver) Stop()
func (d *Driver) ReadInputs() (nio.Values, error)       // io.Driver
func (d *Driver) ReadInputsInto(nio.Values) error       // io.BatchReader
func (d *Driver) WriteOutputs(nio.Values) error
func (d *Driver) Quality() map[string]nio.Quality       // io.QualityReporter
func (d *Driver) InputNames() []string                  // and OutputNames()
func (d *Driver) ScanClasses() map[string][]string
func (d *Driver) Health() Health                        // → drivers.go modbusStatus()

type Health struct {
	Sources               []SourceHealth
	Reads, Writes, Errors uint64
}
type SourceHealth struct {
	ID, Addr, State string  // State: connected | connecting | error | parked
	SinceMs         int64
	LastError       string
	Retries         uint64
	RTTMs           float64 // EWMA of request round-trip
	Blocks          int     // read blocks per cycle
	Exceptions      uint64  // Modbus exception responses
}
```

Internal seam that makes it testable without a socket, same idea as
`sparkplug/host`'s `handleMessage`: `tcp.go` exposes
`type conn interface{ Request(ctx, unit, fc, pdu) ([]byte, error) }`; `plan.go`
and `encode.go` are pure functions over `[]uint16` / `[]bool`.

## 2. Wire & state machine

**Per source, one goroutine, one TCP connection, requests strictly sequential.**
Modbus TCP allows pipelining by transaction id, but most protocol gateways
(Anybus, ADAM) do not; sequential is the safe default and still an order of
magnitude faster than one-request-per-variable polling (§3).

```
parked ──(Enable tag true / no Enable tag)──▶ connecting ──dial ok──▶ connected
   ▲                                              │ ▲                    │
   └────────── Enable false ──────────────────────┘ └── backoff 1s→60s ──┘ (dial/IO error, 3 consecutive exceptions on a block)
```

- `New` builds the read plan and validates addresses/formats offline —
  `naut check` and `build` pass with no device in sight (the
  `eip`/`sparkplug-host` rule).
- `Start` launches one loop per source. Each loop: for each scan class due,
  execute its block reads, decode into a per-source snapshot under a mutex;
  then flush pending writes (§4).
- A source that fails **parks its tags at Quality NotConnected** (never
  delivered) or **Stale** (delivered once, now silent); last values hold — the
  tag store never sees a zero because a cable was pulled. Same contract
  `sparkplug-host` gives. **Reads of a never-delivered tag fault the scan** as
  they do there; the generated `<source>__Online` companion BOOL
  (driver-synthesised, like `<site>__Online`) is the guard, and `nautilus
  check` can warn when a program reads a Modbus tag unguarded in the same task.
- Exception responses (illegal address/function/value) are **per block**: the
  block is marked, its tags go Bad, the rest of the source keeps polling. Three
  consecutive exceptions on a block park that block for one backoff period —
  which turns an address collision (two variables claiming one register, a
  mis-transcribed datasheet) into a visible `/api/drivers` row rather than
  silent garbage.
- Timeout per request (`Timeout`); a timeout counts as an IO error → reconnect
  with backoff (tentacle's `retryMinDelay`/`retryMaxDelay` semantics).

## 3. Read plan: block reads instead of one request per variable

The naive Modbus client — and the framework this driver replaces — issues **one
request per variable per interval, sequentially**: a 16-channel analyser is 16
requests per second, a fifteen-device skid is tens of them across as many
sockets, and the poll period is bounded by the sum of the round-trips rather
than by the devices. `plan.go` instead groups bindings by `(source, table, scan class)`, sorts by address,
and coalesces into blocks of ≤125 registers / ≤2000 coils with a **gap
tolerance** (default 8 registers — reading two unused words is cheaper than a
second round-trip). That analyser becomes **one** FC3/FC4 request for regs
0–57; a PID loop is one request for 0–9 plus one for the setpoint at 262.

Options: `WithBlockGap(n)`, and per-source `max-block` for devices that reject
>N registers (some ADAM firmware caps at 64). `Health.Blocks` shows the
resulting count; `naut modbus import --plan` prints the plan so a
commissioning tech sees exactly which requests will hit a device.

## 4. Writes

Output tags = `Writable: true` bindings. Contract identical to `eip` and the
host driver: **outputs are commands; the runtime's write to the tag propagates
on change**, coalesced per source into FC16 (contiguous holding) / FC15
(contiguous coils) / FC6 / FC5 for singles.

Two behaviours need explicit forms because devices depend on them:

1. **Periodic re-assert.** Many devices treat a written word as a keep-alive
   and revert or fault if it stops arriving: an i550 VFD control word refreshed
   every second, a Banner SC10 that drops its enable if the safety word is not
   refreshed every 2.5 s, setpoints behind a gateway that forgets on a power
   blip. `Rewrite: 2.5s` re-sends the last commanded value on that period even
   when unchanged. **Default 0 (change-only)** — with the baseline rule from
   `sparkplug-host`: the first snapshot after `Start` is a baseline and is
   never written unless the binding has `Rewrite` (a safety word *must* be
   written on connect).
2. **Write-only vs read-back.** Frameworks that make a variable
   one-directional force an `X_Tsp` (write) / `X_Tsp_read` (read) pair at one
   register. Here a binding can be `Writable: true` **and** polled — the
   register is in the read plan, the tag reads back what the device holds, and
   the runtime's "outputs adopt the live value" rule makes that read-back the
   source of truth after a restart, removing by construction the classic bug
   where a retained broker value re-applies a stale setpoint on reconnect.
   `write-only: true` opts out for registers that are not readable (rare; some
   VFD control words).

Writes to a parked/disconnected source are **queued per source (last value per
tag)** and flushed on reconnect — same shape as the host driver's per-node
write queue, reported as `queued writes` on the device row.

## 5. Manifest, schema, project wiring

```yaml
# nautilus.yaml
drivers:                                  # B: list form; `driver:` (singular) stays as sugar for one
  - type: modbus
    manifest: modbus_manifest.yaml        # generated
    scan-rate: 1s                         # default class
    scan-classes: {fast: 1s, slow: 2.5s}
    tag-classes: {slow: ["RGN_*", "HTDL_*", "SC_*"]}
tag-files: [tags/modbus.yaml]
```

`modbus_manifest.yaml` (generated; same no-struct-tags/`KnownFields(true)`
convention as `eip_manifest.yaml`, block style):

```yaml
# Generated by `naut modbus import --map devices.yaml`
sources:
  - id: FTIR_I
    host: 192.0.2.51
    port: 502
    unitid: 1
    wordorder: little          # tentacle reverseWords: true
    timeout: 3s
    enable: CFG.FtirEnabled    # BOOL tag; parks the source when false
  - id: RGN_PID_A
    host: 192.0.2.10
    unitid: 1
tags:
  - {name: FTIR_I_CO,     source: FTIR_I,    table: holding, address: 14,  format: float32}
  - {name: RGN_PID_A_Tpv, source: RGN_PID_A, table: holding, address: 0,   format: int32, scale: 1}
  - {name: RGN_PID_A_Tsp, source: RGN_PID_A, table: holding, address: 262, format: int32, writable: true, rewrite: 2.5s}
  - {name: FAN_Control,   source: FAN,       table: holding, address: 2100, format: uint16, writable: true, rewrite: 1s}
  - {name: SC_PM_V01,     source: SC_PM,     table: holding, address: 8,   format: int16, writable: true, rewrite: 2.5s, writeonly: true}
```

`tags/modbus.yaml` is the ordinary tag file (`role: input|output`, `type`,
`unit`, `desc`) composed with `tag-files:` — same as `--tags-out` in
`eip`/`sparkplug import`.

**Struct bindings (optional).** `format: OmronLoop` where `OmronLoop` is a TYPE
from `modbus_types.st`, with the binding carrying `members: {Tpv: {address: 0,
format: int32}, Tsp: {address: 262, writable: true, rewrite: 2.5s}, …}`. The
tag is then one struct (`RGN_PID_A : OmronLoop`), publishes as one Sparkplug
Template, and the HMI faceplate binds one tag. Member-level writes reuse the
host driver's partial-template machinery (`internal/project` already routes
`Tag.MEMBER` writes).

**Project wiring** (`internal/project/project.go`): `DriverConfig` needs **no
new keys** — `manifest`, `scan-rate`, `scan-classes`, `tag-classes` all already
exist for `eip`. Add `case "modbus":` in the driver switch; `drivers.go` gains
`modbusStatus(Health)` (kind `modbus`, one `DriverDevice` per source,
`VolatileExtra`: `rtt`, `reads`, `retries`). Schema:
`tools/vscode-iec/schemas/nautilus.schema.json` enum + `schema_test`. `runcmd.go`
needs nothing — it already asserts `Start(ctx)`/`Stop()`.

### B. `drivers:` — multi-driver

`io.Multi` (in `io/`): `NewMulti(named ...NamedDriver)`; `ReadInputsInto` fans
out and merges (a tag name owned by two drivers is a load-time error naming
both); `WriteOutputs` routes by `OutputNames()`; `Quality()` merges;
`Start/Stop` fan out; each child keeps its own `DriverStatus` row —
`/api/drivers` already returns a list. `project.go`: `Drivers []DriverConfig`
with `yaml:"drivers"`; `driver:` + `drivers:` both set = error, `driver:` alone
= one-element list. Sparkplug `device:` (DBIRTH tracks "the field driver's
health") becomes **all drivers healthy**. ≈1 day; unblocks any second bus.

## 6. `naut modbus` CLI

| Subcommand | Does |
|---|---|
| `import --map devices.yaml [--out dir] [--tags-out tags/modbus.yaml] [--writable globs] [--plan]` | offline: a **device map** (register maps + scaling, one entry per device *type*; instances with host/unit-id) → `modbus_manifest.yaml` + `tags/modbus.yaml` (+ `modbus_types.st` for struct bindings). Byte-identical on re-run. `--plan` prints the block-read plan |
| `browse --host --unit --table --from --count [--format]` | live poke: read a range, print raw + decoded in every format — the commissioning tool most stacks never ship |
| `serve --manifest modbus_manifest.yaml [--listen :5020] [--source id] [--values tags.json] [--ramp]` | the in-repo slave: serves every source in a manifest as a Modbus TCP server (multi-unit-id on one port, or one port per source); values from a JSON file or built-in ramps — hardware-in-the-loop without hardware |
| `tags` | regenerate the tag file only (mirrors `eip tags`) |

The device map is ordinary content when written for generic devices (an Omron
E5CC, an ADAM-4018, a Lenze i550 — all public register maps) and lives in
`examples/modbus/`.

## 7. Testing

1. **Unit, no socket.** `encode_test.go` table: every format × word/byte order ×
   scale, round trip through `[]uint16`. `plan_test.go`: coalescing, gap
   tolerance, 125-register cap, mixed tables, classes. `tcp_test.go`: MBAP
   framing against golden byte strings (incl. exception responses, short reads,
   transaction-id mismatch, wrong unit id).
2. **Driver against `modbus/slave`** (in-process, `127.0.0.1:0`, no build tags
   — the `eip/logixserver` precedent): tags land; a write becomes
   FC16/FC6/FC5/FC15 on the wire; `Rewrite` fires on schedule and only there;
   kill the slave → Stale + `__Online` false, values hold, scan never faults;
   restart → recovery + queued write delivered; exception on one block leaves
   the others Good; three exceptions park the block and it un-parks after
   `RetryMin`; `Enable` false parks the source.
3. **Golden generator test** (`cmd/naut/modbus_test.go`):
   `testdata/devices.yaml` → the three files byte-for-byte; `st.Parse` +
   `st.Lower` the generated `modbus_types.st`.
4. **`examples/modbus/`** — a generic three-device project (temperature
   controllers, VFD, analyser) with its `*_test.yaml` in virtual time, covered
   by `check_manifest_test.go`.
5. **A foreign implementation, in CI.** Everything above tests our encoder
   against our decoder. A pymodbus server seeded with known raw words for every
   format in both word orders, plus an unimplemented range answering exception
   0x02 and an optional per-request latency, is driven by the real `Driver`:
   decode agreement, write round trip, exception → block `bad` with siblings
   Good, latency above `Source.Timeout` → reconnect backoff with values held.
   Gated on an env var like `sparkplug/conformance_test.go`, its own CI job.
6. **Real hardware, once.** `browse` the device for word order and addressing,
   confirm exception behaviour on an unimplemented register, pull the cable and
   watch the reconnect, and record the quirks in the guide.

## 9. Risks & open questions

1. **Device-side watchdog and fail-safe values.** A keep-alive that lives only
   in the master is half a safety story. Where the device offers a Modbus
   watchdog with configurable fail-safe outputs (many remote-I/O and IO-Link
   masters do), configure it at commissioning and record the safe state per
   output alongside the device map — then risk 3's gate *causes* the fail-safe
   instead of the driver masking a fault by refreshing a stale command.
2. **Protocol gateway semantics.** A gateway typically presents one unit-id per
   device behind it, and the same hardware has shipped with opposite word order
   between firmware revisions — one deployment's `reverseWords: true` is
   another's `false` for the identical part number. `browse` on a live gateway
   settles it; word order must therefore be per source (it is).
3. **Rewrite cadence and safety words.** A controller that drops enables when
   its word is not refreshed must never see a *stale* command either: the
   `Rewrite` loop must stop re-asserting when the **program** is not running
   (scan faulted, standby replica). Gate it on `runtime.Coordinator` leadership
   and on a "logic healthy" signal — the same gate the host driver's STATE
   uses. Design it in, do not retrofit. (`Driver.SetWriteGate`.)
4. **Multi-register values across a device's implemented range.** A block that
   straddles the end of an implemented range returns an exception for the
   *whole* block. `max-block` per source and gap tolerance 0 are the escape
   hatches; `import --plan` makes the plan visible before it hits a device.
5. **Scan rate vs. device latency.** A controller behind a gateway answers in
   ~50–100 ms; nine of them on one gateway at 2.5 s is fine sequentially, at
   250 ms it is not. Scan classes per source, and `Health.RTTMs` on the device
   row so it is measurable rather than guessed.
6. **Redundancy.** Two replicas must not both poll a safety controller or write
   a VFD control word. Same rule as the host: only the leader's driver `Start`s
   I/O; the standby stays parked. `runtime.Coordinator` already gates the scan
   loop and `WriteOutputs`; confirm it gates driver start too.
7. **Formats out of scope for this wave:** `UInt64`/`int64`, BCD and string.
   `UInt32` above 2^31 is fine (it widens into the int64 tag).
8. **Modbus RTU / serial** — out of scope. A `serial:` source form can be added
   later; `tcp.go`'s framing is the only thing that changes.
9. **Open: does `tag-classes:` glob on the nautilus name or the device
   address?** `eip` globs on the nautilus name; keep that.
10. **Open: block-typed bindings in the generator** — structs by default, or
    flat tags with structs opt-in? Recommend structs for device families that
    exist as TYPEs (`OmronLoop`, `Vfd`, `FtirChannels`), flat for singletons.
