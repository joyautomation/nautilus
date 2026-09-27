# Design brief: IT hardware drivers for nautilus (SNMP, Redfish, Prometheus)

Status: **design 2026-09-26; `hw/` built the same day** (types table +
`hw_types.st`, `Binding`/`Expr`/`Counter`, `Base` with tests through the real
runtime). Protocol packages, codegen and the example are next. Written on the `it-drivers`
branch from `randd/handoffs/IT-DRIVERS-HANDOFF.md`; the UDT set in §2 is the
contract the `spatial-hmi` branch binds its `server`, `switch` and
`switch-port` scene nodes to, so §2 is frozen once this brief merges and every
later change to it is a versioned decision, not a refactor.

**Goal:** IT hardware becomes **ordinary nautilus tags**. A switch port is a
UDT instance, a failed PSU is an ISA-18.2 alarm, a rack publishes Sparkplug B
like a pump skid. Then the 3D view, phone AR, alarms, history and Sparkplug
all work on a server rack with no special cases — and the server/NOC tier
(the GB-style clusters) gets its monitoring from the same tool as the plant.
Three manifest-tier drivers, one shape:

| `driver.type` | Speaks to | Package | Dependency |
|---|---|---|---|
| `snmp` | switches, PDUs, UPSes (IF-MIB, ENTITY-SENSOR-MIB, UPS-MIB, vendor MIBs) | `snmp/` | `github.com/gosnmp/gosnmp` — inside the package only, the way paho stays inside `sparkplug/` |
| `redfish` | server BMCs (DMTF Redfish: Systems, Chassis, Thermal/Power and their 2021+ ThermalSubsystem/PowerSubsystem successors) | `redfish/` | none — JSON over HTTPS is `net/http` + `encoding/json` |
| `prometheus` | commodity hosts with no BMC, scraped straight from `node_exporter`'s `/metrics` text format | `prom/` | none — the exposition text format is a 150-line parser |

Plus **`hw/`**, the shared substrate all three sit on: the UDT table
(§2), the per-source poll loop with backoff and quality (§4), counter→rate
(§5), and the manifest vocabulary they share (§6). `hw/` is pure stdlib.
`modbus/` and `docs/design/modbus.md` are the template this brief copies:
wire layer, manifest, per-source polling and quality, an in-process test
server, codegen via `naut <proto> import|browse|serve|tags`, and a foreign
implementation in CI before any hardware is touched.

## 1. Package layout & public API

```
hw/
  types.go      the UDT set as ir-independent Go tables → Types() renders hw_types.st
  poll.go       Source loop: interval, timeout, backoff 1s→60s, parked/connecting/connected/error,
                per-source snapshot under a mutex, __Online companion, stale-after
  rate.go       Counter: (value, t) pairs → per-second rate; 32/64-bit wrap, reset detection
  bind.go       Binding vocabulary shared by the three manifests: map/eq/scale/offset/rate/width
  status.go     SourceHealth → server.DriverStatus (one device row per source)
snmp/
  client.go     gosnmp behind a `getter` interface (Get/GetBulk/Walk/Set) so tests need no socket
  manifest.go   Manifest{Sources, Tags}; KnownFields; v2c/v3 credentials by env or file
  driver.go     io.Driver + BatchReader + QualityReporter; hw.Source per SNMP agent
  profiles/     what MIB objects make a SwitchPort/Switch/PDU/UPS — codegen-time knowledge only
  codegen/      walk (live or recorded) → snmp_manifest.yaml + tags/snmp.yaml + hw_types.st
  agent/        in-repo SNMP agent that replays a recorded walk (tests; `naut snmp serve`)
redfish/
  client.go     net/http + sessions or basic auth; TLS options; `fetcher` interface for tests
  manifest.go   Manifest{Sources, Tags}; resource + path bindings
  driver.go     io.Driver …; one hw.Source per BMC; a poll fetches each bound resource once
  codegen/      live service root or a DMTF mockup tree → redfish_manifest.yaml + tags + types
  mockup/       serve a DMTF mockup directory over HTTP (tests; `naut redfish serve`)
prom/
  text.go       exposition text format parser → samples (name, labels, value)
  expr.go       the tiny arithmetic over named selectors that CpuPct/MemPct need (§6.3)
  manifest.go   Manifest{Sources, Tags}
  driver.go     io.Driver …; one hw.Source per scrape URL
  codegen/      a live /metrics or a recorded metrics.txt → prometheus_manifest.yaml + tags + types
  serve/        serve a recorded metrics.txt (tests; `naut prometheus serve`)
```

Every driver has the same Go surface as `modbus`, so `internal/project`
wires all three with one code shape:

```go
func New(m Manifest, opts ...Option) (*Driver, error)   // NEVER dials; validates bindings offline
func (d *Driver) Start(ctx context.Context)             // runcmd.go asserts this
func (d *Driver) Stop()
func (d *Driver) ReadInputs() (nio.Values, error)       // io.Driver
func (d *Driver) ReadInputsInto(nio.Values) error       // io.BatchReader
func (d *Driver) WriteOutputs(nio.Values) error         // §7: off unless a binding opts in
func (d *Driver) Quality() map[string]nio.Quality       // io.QualityReporter
func (d *Driver) InputNames() []string                  // and OutputNames() — io.Multi routing
func (d *Driver) ScanClasses() map[string][]string
func (d *Driver) Health() hw.Health                     // → drivers.go: one row per source

// hw — shared
type Health struct { Sources []SourceHealth; Polls, Errors, Writes uint64 }
type SourceHealth struct {
	ID, Addr, State string   // State: connected | connecting | error | parked
	SinceMs         int64
	LastError       string
	Retries         uint64
	RTTMs           float64  // EWMA of one poll (all requests for one source)
	LastPollMs      int64    // wall clock of the last complete poll
	Requests        int      // requests per poll (SNMP PDUs, HTTP GETs, 1 scrape)
}
```

Options mirror modbus: `WithScanRate` (the default poll interval),
`WithScanClass(name, interval)`, `WithTagClass(class, globs...)`,
`WithLogger`, and a per-package test seam (`WithGetter`, `WithFetcher`,
`WithScraper`) that replaces the transport.

## 2. The UDT set — the contract

Declared once in `hw/types.go`, rendered into a project as **`hw_types.st`**
by any of the three importers (the same bytes whichever one writes it, so a
project with a switch over SNMP and a host over Prometheus has one types
file). Member names, types and units are the contract for: the alarm rules
(`match: {type, member}`), the Sparkplug Templates (a struct tag publishes as
one Template named after its type), and the 3D scene nodes on `spatial-hmi`.

**Flat instances, not arrays.** `SW1_Port01 : SwitchPort` rather than
`SW1.Ports : ARRAY[1..28] OF SwitchPort`. Three reasons, all in machinery
that exists today: `type:` in a tag file names a struct, not an array
(`tags.md` §8 lists array-typed tags as deferred); alarm rules enumerate
`(type, member)` over **struct tags** (`alarm/def.go`), so one `SwitchPort.Down`
rule reaches every port only if every port is its own tag; and Sparkplug
Templates are per struct, so a port is one Template instance an Ignition
host browses by name. The cost is a tag count (a 48-port switch is 49 tags)
that the store, the delta stream and the alarm table are all built for.

**Naming.** The device root is the source id; children are
`<id>_<Kind><NN>` with `NN` zero-padded to the width of the largest index on
that device (`Port01…Port28`, `Fan1…Fan6`, `PSU1`, `Outlet01…Outlet08`);
temperature sensors take the sanitised sensor name instead of a number
(`NODE1_Temp_CPU`, `NODE1_Temp_Inlet`) because a number means nothing on a
BMC. `[A-Za-z0-9_]` only; the importer sanitises and reports collisions.
`alarms.site-from: "^([A-Za-z0-9]+?)(?:_|$)"` then gives every child tag
its device as `{site}`, which is what `enable: "{site}__Online"` wants.

**Companions.** The driver synthesises `<id>__Online : BOOL` per source
(the `modbus`/`sparkplug-host` contract: true while polls are answered) and
`<id>__LastPollMs : DINT`. Reads of a never-delivered tag fault the scan, as
in both existing drivers; `__Online` is the guard.

**Provenance.** A member every source fills is plain; a member only some
sources can fill says so — it stays at zero-of-field elsewhere, never
guessed. Rates are per second and `Pct` members are 0–100.

### `Server` — one physical or virtual machine (Redfish BMC, or node_exporter on the host)

| Member | Type | Unit | Redfish | Prometheus (node_exporter) |
|---|---|---|---|---|
| `Online` | BOOL | | source answered its last poll | same |
| `PowerOn` | BOOL | | `Systems/{id}.PowerState == "On"` | always true while scraped |
| `Health` | INT | 0 ok, 1 warning, 2 critical, 3 unknown | `Systems/{id}.Status.Health` (OK/Warning/Critical) | worst of the alarms below |
| `Fault` | BOOL | | `Health == 2` | `Health == 2` |
| `Warning` | BOOL | | `Health == 1` | `Health == 1` |
| `Model` | STRING | | `Systems/{id}.Model` | `node_uname_info{nodename}` |
| `Serial` | STRING | | `Systems/{id}.SerialNumber` | `node_dmi_info{product_serial}` when present |
| `UptimeS` | DINT | s | — (0) | `time() − node_boot_time_seconds` |
| `CpuPct` | REAL | % | `Systems/{id}.ProcessorSummary.Metrics` when the BMC has it, else 0 | `100 − 100·rate(idle)/cpus` |
| `MemPct` | REAL | % | — (0) | `100·(1 − MemAvailable/MemTotal)` |
| `Load1` | REAL | | — (0) | `node_load1` |
| `RootDiskPct` | REAL | % | — (0) | `100·(1 − avail/size)` on `mountpoint="/"` |
| `InletTempC` | REAL | °C | the inlet/ambient sensor | hwmon sensor named inlet/ambient, else 0 |
| `MaxTempC` | REAL | °C | max over the chassis' temperatures | max over `node_hwmon_temp_celsius` |
| `PowerW` | REAL | W | `Chassis/{id}.PowerSubsystem` (or legacy `Power.PowerControl[0].PowerConsumedWatts`) | — (0) |
| `FanCount`, `PsuCount`, `TempCount` | INT | | counts of the child tags generated | same |

### `Fan`

| Member | Type | Unit | Redfish | Prometheus |
|---|---|---|---|---|
| `Name` | STRING | | `Fans[].Name` | `node_hwmon_fan_rpm{sensor}` label |
| `Present` | BOOL | | `Status.State != "Absent"` | metric present |
| `RPM` | REAL | rpm | `Reading` (or `SpeedRPM`) | value |
| `Pct` | REAL | % | `Reading` when `ReadingUnits == "Percent"`, else 0 | 0 |
| `Fault` | BOOL | | `Status.Health != "OK"` or `Reading < LowerThresholdCritical` | `RPM == 0 && Present` |

### `PSU`

| Member | Type | Unit | Redfish | SNMP (PDU/UPS have none; a switch's PSU rows via ENTITY-STATE / vendor MIB) |
|---|---|---|---|---|
| `Name` | STRING | | `PowerSupplies[].Name` | entPhysicalName |
| `Present` | BOOL | | `Status.State != "Absent"` | entity present |
| `Fault` | BOOL | | `Status.Health != "OK"` | vendor status ≠ ok |
| `InputOk` | BOOL | | `Status.State == "Enabled"` and `LineInputVoltage > 0` | vendor |
| `InputV` | REAL | V | `LineInputVoltage` | 0 |
| `OutputW` | REAL | W | `PowerOutputWatts` (or `LastPowerOutputWatts`) | 0 |
| `CapacityW` | REAL | W | `PowerCapacityWatts` | 0 |

### `TempSensor`

| Member | Type | Unit | Source |
|---|---|---|---|
| `Name` | STRING | | sensor name |
| `Value` | REAL | °C | reading |
| `Fault` | BOOL | | reading absent/invalid on a present sensor |
| `High` | BOOL | | reading ≥ `HighSP` (Redfish UpperThresholdNonCritical; ENTITY-SENSOR / vendor warning; node_exporter `node_hwmon_temp_max_celsius`) |
| `HighHigh` | BOOL | | reading ≥ `HighHighSP` (UpperThresholdCritical; `node_hwmon_temp_crit_celsius`) |
| `HighSP`, `HighHighSP` | REAL | °C | the device's own thresholds, 0 when it has none — the importer may set a project default instead |

### `Switch` — the chassis of a managed switch (SNMP)

| Member | Type | Unit | SNMP |
|---|---|---|---|
| `Online` | BOOL | | polls answered |
| `Name` | STRING | | `sysName.0` |
| `Model` | STRING | | `entPhysicalModelName` of the chassis, else `sysDescr.0` |
| `Serial` | STRING | | `entPhysicalSerialNum` of the chassis |
| `UptimeS` | DINT | s | `sysUpTime.0 / 100` |
| `CpuPct`, `MemPct` | REAL | % | vendor MIB (FS/Broadcom: to confirm on the S3900); 0 without one |
| `TempC` | REAL | °C | ENTITY-SENSOR-MIB celsius row, else vendor MIB, else 0 |
| `Fault` | BOOL | | any PSU/Fan child fault, or vendor chassis status |
| `PortsTotal`, `PortsUp` | INT | | count of generated `SwitchPort` children; those with `OperUp` |

### `SwitchPort` — one interface (IF-MIB), the AR target

| Member | Type | Unit | SNMP |
|---|---|---|---|
| `Index` | DINT | | `ifIndex` |
| `Name` | STRING | | `ifName`, else `ifDescr` |
| `Alias` | STRING | | `ifAlias` — the label the operator typed |
| `AdminUp` | BOOL | | `ifAdminStatus == up(1)` |
| `OperUp` | BOOL | | `ifOperStatus == up(1)` |
| `Down` | BOOL | | `AdminUp && !OperUp` — the alarm member: an enabled port with no link |
| `SpeedMbps` | REAL | Mb/s | `ifHighSpeed` (else `ifSpeed / 1e6`) |
| `InBps`, `OutBps` | REAL | bit/s | `rate(ifHCInOctets) · 8` (falls back to `ifInOctets`, width 32) |
| `InPct`, `OutPct` | REAL | % | `Bps / (SpeedMbps · 1e6)` |
| `InErrors`, `OutErrors` | DINT | | `ifInErrors`, `ifOutErrors` (counter values) |
| `InDiscards`, `OutDiscards` | DINT | | `ifInDiscards`, `ifOutDiscards` |
| `ErrorRate` | REAL | 1/s | `rate(ifInErrors + ifOutErrors)` |
| `PoeOn` | BOOL | | POWER-ETHERNET-MIB `pethPsePortDetectionStatus == deliveringPower(3)`; false without PoE |
| `PoeW` | REAL | W | vendor/LLDP-MED power MIB when present, else 0 |

### `PDU` and `PDUOutlet` — a switched rack PDU (SNMP; CyberPower CPS-MIB first)

| Member | Type | Unit | Notes |
|---|---|---|---|
| `PDU.Online` | BOOL | | |
| `PDU.Name`, `Model`, `Serial` | STRING | | |
| `PDU.Amps`, `PDU.Watts` | REAL | A, W | total load |
| `PDU.Overload` | BOOL | | vendor load-state ≠ normal |
| `PDU.OutletCount` | INT | | |
| `PDUOutlet.Index` | DINT | | |
| `PDUOutlet.Name` | STRING | | outlet label |
| `PDUOutlet.On` | BOOL | | read-back of the outlet state |
| `PDUOutlet.Amps`, `Watts` | REAL | A, W | metered outlets only, else 0 |

A command is **not** a member: it is a separate scalar output tag
(`PDU1_Outlet03_Cmd : BOOL`, §7), the sparkplug-host rule for writable UDT
members, so the struct tag stays an input and `On` reads back what the
device holds.

### `UPS` (SNMP; RFC 1628 UPS-MIB, CyberPower CPS-MIB)

| Member | Type | Unit | Notes |
|---|---|---|---|
| `Online` | BOOL | | |
| `Name`, `Model`, `Serial` | STRING | | |
| `OnBattery` | BOOL | | `upsOutputSource == battery(5)` |
| `LowBattery` | BOOL | | `upsBatteryStatus == batteryLow(3)` |
| `BatteryFault` | BOOL | | `upsAlarmsPresent` includes a battery alarm, or vendor replace-battery flag |
| `Fault` | BOOL | | any alarm present |
| `ChargePct` | REAL | % | `upsEstimatedChargeRemaining` |
| `RuntimeMin` | REAL | min | `upsEstimatedMinutesRemaining` |
| `LoadPct` | REAL | % | `upsOutputPercentLoad.1` |
| `InputV`, `OutputV` | REAL | V | `upsInputVoltage.1`, `upsOutputVoltage.1` |
| `BatteryTempC` | REAL | °C | `upsBatteryTemperature`, 0 when absent |

**What is deliberately not here:** per-CPU/per-disk detail (a `Disk` UDT is
the obvious next type; wait for a project that needs it), interface
VLAN/LLDP topology (that is a graph, not a tag), and any vendor-specific
member. Add members, never rename them.

## 3. Per-protocol mapping

**All MIB, schema and metric-name knowledge lives in codegen.** At run time
a driver is a dumb executor of explicit bindings — an OID, a Redfish resource
path, a metric selector — the way `modbus.Driver` knows registers and
nothing about VFDs. The manifest is therefore self-describing on review,
regeneration is byte-identical, and a device the profiles have never seen is
handled by editing the manifest, not the driver.

### 3.1 SNMP

- **Transport:** v2c and v3 (USM: SHA-1/SHA-256 auth, AES-128/256 priv;
  MD5/DES accepted with a warning because half the PDUs on the market still
  ship with them). One UDP socket per source, requests sequential, GetBulk
  for tables with `max-repetitions` per source (default 20; some agents
  choke above 10 — `max-repetitions:` per source is the knob, like modbus'
  `max-block`). Timeout and retries per source (default 3s × 2).
- **Poll:** the driver groups a source's bound OIDs by scan class and
  issues Get for scalars and GetBulk over each table column it needs (ifTable
  columns for the port set, not one Get per port per member). A 28-port
  switch with 14 members per port is ~14 GetBulk walks per poll, ~3 PDUs
  each: measured on the S3900 before defaults are frozen (§9.5).
- **Profiles** (`snmp/profiles`): `switch` (IF-MIB + ifXTable + ENTITY-MIB
  chassis row + ENTITY-SENSOR-MIB + POWER-ETHERNET-MIB, with vendor
  extensions keyed by `sysObjectID` prefix — the FS S3900 first),
  `pdu-cyberpower`, `ups-rfc1628`, `ups-cyberpower`. A profile is a table:
  UDT member ← OID column, plus the `map`/`scale`/`rate` vocabulary of §6.
  `naut snmp import` walks the device (or a recorded walk), picks the profile
  from `sysObjectID` unless `--profile` says otherwise, enumerates the
  instances (ifIndex rows, outlet rows, sensor rows), and writes explicit
  per-member bindings. Ports filtered by `--ports` (default: physical
  ethernet, `ifType == 6`, skipping loopback/VLAN interfaces).
- **Binding:** `{oid: 1.3.6.1.2.1.31.1.1.1.6.3, rate: true, width: 64, scale: 8}`.

### 3.2 Redfish

- **Transport:** HTTPS with a session token (`POST /redfish/v1/SessionService/Sessions`,
  re-created on 401) or basic auth when the BMC refuses sessions. TLS: BMCs
  ship self-signed certs, so per source `tls: {insecure: true}` or
  `tls: {ca-file: …}`; the default verifies and the error names the fix.
  One connection per source, requests sequential, `Timeout` default 10s.
- **Poll:** a source lists the resources its bindings need
  (`/redfish/v1/Systems/1`, `/redfish/v1/Chassis/1/Thermal`, …); each is
  fetched once per poll, every member bound to it decoded from the one
  body. Scan classes let `Systems/1` (power state, 5s) poll faster than
  `Chassis/1/Thermal` (30s). BMCs rate-limit: default interval 10s, and
  §9.4 records what each BMC tolerates.
- **Schema drift:** Redfish 2021.x deprecated `Thermal` and `Power` in favour
  of `ThermalSubsystem`/`PowerSubsystem` with `Fans`/`PowerSupplies`
  collections and `Sensors`. `import` probes the service root and emits
  bindings for whichever the BMC serves (the new form when both exist).
  The **mockups** in CI cover both generations.
- **Binding:** `{resource: /redfish/v1/Chassis/1/Thermal, path: "Fans[MemberId=0].Reading"}`.
  The path grammar is dotted properties with one selector form
  `[Key=Value]` on arrays, chosen over positional indexes because a BMC
  reorders arrays across firmware. `map: {OK: 0, Warning: 1, Critical: 2}`
  turns an enum into `Health`; `eq: Critical` turns it into a BOOL.

### 3.3 Prometheus (node_exporter)

- **Transport:** plain HTTP GET of one `/metrics` URL per source, parsed as
  the exposition text format (`# TYPE`, labels, values; histograms and
  summaries parsed but not bound). Default interval 15s.
- **Profile** `node`: the `Server` members from `node_cpu_seconds_total`,
  `node_memory_*`, `node_filesystem_*`, `node_hwmon_*`, `node_boot_time_seconds`,
  `node_uname_info`; `Fan`/`TempSensor` children from hwmon (one per
  `{chip,sensor}`). Metric names move between node_exporter releases; the
  profile records which version it was written against and `import`
  reports a bound metric the scrape does not carry.
- **Binding:** `{metric: node_hwmon_temp_celsius, labels: {chip: "platform_coretemp_0", sensor: "temp1"}}`;
  a `Server` member that is arithmetic over several series uses `expr`
  (§6.3). Out of scope: querying a Prometheus *server* (`/api/v1/query`).
  The driver scrapes exporters directly, so a host is monitored with no
  Prometheus deployment at all; a server-backed source is a later `url:`
  form, not a redesign.

## 4. Poll loop, state machine, quality

Per source, one goroutine, one transport, requests strictly sequential —
`hw.Source` owns it; the protocol packages supply a `poll(ctx) (samples,
error)` closure. Intervals are **seconds**, never the scan rate: a
controller scanning at 100ms reads the same snapshot ~100 times between
polls, and that is correct (a switch's counters do not move faster than the
device's own 5–10s sampling).

```
parked ──(Enable tag true / none)──▶ connecting ──first full poll ok──▶ connected
   ▲                                       │ ▲                              │
   └──────── Enable false ─────────────────┘ └── backoff 1s→60s ────────────┘ (timeout, refused, auth failure, 5xx)
```

- `New` validates every binding offline (OID syntax, path grammar, metric
  selector, `map`/`eq` types against the UDT member) — `naut check` and
  `build` pass with no device in sight.
- A **complete** poll (every request answered) publishes a fresh per-source
  snapshot and sets `__Online` true. A poll where *some* requests failed
  publishes what came back, marks the tags whose requests failed **Bad**,
  and counts an error; three consecutive failed polls → `error` state →
  backoff, tags **Stale**, values hold. Unreachable at start → tags
  **NotConnected** until the first delivery.
- **`stale-after`** (default 3× the interval): a source whose last complete
  poll is older than this reports Stale even while "connected" — a hung
  agent that accepts the socket and never answers is the Modbus lesson
  ("connected" and "heard from" are different facts).
- Bad is tracked **per tag per scan class**: a port whose counters (fast
  class) came back but whose status row (slow class) failed is Bad as a
  whole, and clears once every class that touches it has answered.
- A member absent from an answered resource (a sensor the BMC lists but
  gives `Reading: null`, an OID answering `noSuchInstance`) is **zero-of-field
  and `Present: false`/`Fault: true`** where the UDT has such a member, and
  logged once per source at the first occurrence — not Bad for the whole
  tag, because one dead fan must not grey out the server's power state.
- `Quality()` is answered off the scan loop from the per-source snapshot
  under its mutex (HANDOFF gotcha: it is called from another goroutine).

## 5. Counters → rates

`hw.Counter` keeps `(value, wallclock)` per rate-bound member and delivers
`Δvalue/Δt` per second. Rules: `width: 32|64` (from the profile: `ifInOctets`
is 32, `ifHCInOctets` 64) — a negative delta on a 32-bit counter wraps
(`+2^32`); a negative delta on a 64-bit counter is a **reset** (device
rebooted) and yields no rate for that interval, last rate held. The first
poll has no delta, so **rate members read 0.0 for exactly one interval**
after a source connects; `__LastPollMs` lets logic that cares wait one
interval. Rates use the driver's wall clock, not the runtime Clock: in
`naut test` (virtual time) the driver is replaced by the simulator anyway.

`InPct`/`OutPct` are derived in the driver (`Bps / (SpeedMbps·1e6)`), zero
when speed is unknown, because every consumer wants them and none should
recompute them. Anything beyond that (a moving average, an error budget) is
the project's ST.

## 6. Manifest, schema, project wiring

```yaml
# nautilus.yaml
drivers:
  - type: snmp
    manifest: snmp_manifest.yaml            # generated by `naut snmp import`
    scan-rate: 10s                          # default poll INTERVAL (the eip key, reused)
    scan-classes: {fast: 5s, slow: 60s}     # ports fast, inventory strings slow
    tag-classes: {slow: ["*.Name", "*.Model", "*.Serial", "*.Alias"]}
  - type: prometheus
    manifest: prometheus_manifest.yaml
    scan-rate: 15s
  - type: redfish
    manifest: redfish_manifest.yaml
    scan-rate: 10s
tag-files: [tags/snmp.yaml, tags/prometheus.yaml, tags/redfish.yaml]
alarms:
  site-from: "^([A-Za-z0-9]+?)(?:_|$)"
  rules:
    - { match: {type: SwitchPort, member: Down},  priority: medium, on-delay: 15s, enable: "{site}__Online" }
    - { match: {type: Fan,        member: Fault}, priority: high,   on-delay: 30s, enable: "{site}__Online" }
    - { match: {type: PSU,        member: Fault}, priority: high,   enable: "{site}__Online" }
    - { match: {type: TempSensor, member: HighHigh}, priority: critical, on-delay: 10s, enable: "{site}__Online" }
    - { match: {type: UPS,        member: OnBattery}, priority: high, enable: "{site}__Online" }
```

`DriverConfig` needs **no new keys**: `manifest`, `scan-rate`, `scan-classes`,
`tag-classes` are reused wholesale (their semantics here are poll intervals,
documented as such). `project.go` gains `case "snmp", "redfish", "prometheus":`;
`drivers.go` gains `hwStatus(kind, Health)` (one `DriverDevice` per source,
`VolatileExtra`: `rtt`, `polls`, `retries`, `requests`); schema enum + the
`schema_test` sync; `runcmd.go` needs nothing. `tag-classes:` globs match the
**nautilus tag or dotted member path** (`*.Name`) so inventory strings can be
demoted to the slow class across every device.

### 6.1 The three manifests share one vocabulary

```yaml
# snmp_manifest.yaml (generated; KnownFields; block style)
sources:
  - id: SW1
    host: 192.0.2.2
    port: 161                     # default
    version: 2c                   # 2c | 3
    community-env: SNMP_SW1_COMMUNITY
    # v3 instead:
    # user: nautilus
    # auth: sha256   auth-env: SNMP_SW1_AUTH
    # priv: aes128   priv-env: SNMP_SW1_PRIV
    timeout: 3s
    retries: 2
    max-repetitions: 20
    stale-after: 30s
    enable: CFG.PollSwitch        # optional BOOL tag; false parks the source
tags:
  - name: SW1
    type: Switch
    source: SW1
    members:
      Name:     {oid: 1.3.6.1.2.1.1.5.0}
      UptimeS:  {oid: 1.3.6.1.2.1.1.3.0, scale: 0.01}
      Serial:   {oid: 1.3.6.1.2.1.47.1.1.1.1.11.1}
  - name: SW1_Port01
    type: SwitchPort
    source: SW1
    members:
      Index:    {const: 1}
      Name:     {oid: 1.3.6.1.2.1.31.1.1.1.1.1}
      AdminUp:  {oid: 1.3.6.1.2.1.2.2.1.7.1, eq: 1}
      OperUp:   {oid: 1.3.6.1.2.1.2.2.1.8.1, eq: 1}
      SpeedMbps: {oid: 1.3.6.1.2.1.31.1.1.1.15.1}
      InBps:    {oid: 1.3.6.1.2.1.31.1.1.1.6.1, rate: true, width: 64, scale: 8}
      InErrors: {oid: 1.3.6.1.2.1.2.2.1.14.1}
  - name: PDU1_Outlet03
    type: PDUOutlet
    source: PDU1
    members:
      On:  {oid: 1.3.6.1.4.1.3808.1.1.3.3.3.1.1.4.3, eq: 1}
      Cmd: {oid: 1.3.6.1.4.1.3808.1.1.3.3.3.1.1.4.3, writable: true, set: {true: 1, false: 2}}
```

The binding keys — `map`, `eq`, `scale`, `offset`, `rate`, `width`, `const`,
`writable`, `set`, `scan-class` — are `hw.Binding` fields decoded identically
by all three manifests; only the *locator* differs (`oid` | `resource`+`path` |
`metric`+`labels`/`expr`). Derived members (`Down`, `Health`→`Fault`,
`InPct`) are declared with `derived: "AdminUp && !OperUp"` over sibling
members — a fixed set of derivations the driver implements, named in the
manifest so a reviewer sees them, not a general expression language.

### 6.2 Credentials

Never in `nautilus.yaml` or a manifest. Every secret is named by
`*-env: VAR` (the `sparkplug-host` / `journal.dsn-env` rule) **or**
`*-file: path` for a mounted k8s Secret; both set is an error. `naut check`
warns when a named variable is unset (it cannot fail: check runs on laptops
without the secrets). Redfish: `user`, `password-env`. Prometheus: optional
`bearer-env` / `basic-env` for exporters behind a proxy.

### 6.3 `expr` (Prometheus only)

`CpuPct: {expr: "100 - 100 * idle / cpus", from: {idle: {metric: node_cpu_seconds_total, labels: {mode: idle}, agg: sum, rate: true}, cpus: {metric: node_cpu_seconds_total, labels: {mode: idle}, agg: count}}}`.
Four operators, parentheses, named selectors, `agg: sum|max|min|avg|count`.
It exists because node_exporter's raw series are counters and totals that no
single selector turns into a percentage, and doing that arithmetic in ST
would drag ten exporter-specific members into the `Server` UDT. Kept small
on purpose; not offered to the other two drivers until one needs it.

## 7. Writes: off by default

The generated manifests contain **no writable bindings**. `import` never
emits one; a person adds `writable: true` to `PDUOutlet.Cmd` or
`Server.PowerCmd` by hand, in a reviewed diff, and the tag file's role
becomes `output`. Then:

- **The runtime's existing gates apply unchanged:** `WriteOutputs` runs only
  on the leader (`runtime.Coordinator`), the HTTP write path goes through
  `authorizeWrite` (the write token), and the HMI kit's confirm dialog
  (`confirm.svelte.ts`) fronts every operator write.
- **Semantics match modbus' read-back rule:** `Cmd` is the desired state,
  `On` is what the device reports; the first snapshot after `Start` is a
  baseline never written back (no `Rewrite` here — a PDU that reverts an
  outlet is a fault to alarm on, not a keep-alive to feed). A write to a
  parked source is queued (last value per tag) and delivered on reconnect.
- **Commands are scalar output tags, never UDT members** — `hw.WriteDecl`
  binds `<tag>_Cmd` (a BOOL for an outlet, an INT for server power: 0 none,
  1 on, 2 graceful shutdown, 3 force off, 4 restart, posted as
  `Actions/ComputerSystem.Reset` once on change to non-zero, the program
  returning it to 0) to a (struct tag, member) target. This is the
  sparkplug-host rule for writable template members and keeps the struct an
  input whose member reads back the device's truth.
- **Fencing is not this driver's job.** The home cluster's fence path is
  its own script with its own SNMP credentials and an "error is a failure,
  never probably-off" rule; a Nautilus controller that can power-cycle the
  node it runs on is a loop. Recommendation, recorded here so it is not
  relitigated per project: automatic power writes from control logic are
  **out of scope for v1**; operator-initiated writes only, leader-only,
  confirmed, journaled through the alarm journal like any command. A
  project that wants automatic fencing writes a Go-tier driver with the
  fence protocol's full semantics (STONITH-style confirm-by-read).

## 8. `naut snmp|redfish|prometheus` — codegen parity with modbus

| Subcommand | Does |
|---|---|
| `import --host … \| --walk file.snmpwalk \| --mockup dir \| --file metrics.txt --tag SW1 [--profile switch] [--ports 1-24] [--out dir] [--tags-out tags/snmp.yaml]` | offline or live: enumerate the device, expand the profile into explicit bindings → `<proto>_manifest.yaml` + `tags/<proto>.yaml` + `hw_types.st`. Byte-identical on re-run; `--from` a recorded fixture gives the same bytes as the live device it was recorded from |
| `browse --host … [--oid 1.3.6.1.2.1.2] \| [--path /redfish/v1/Chassis] \| [--url http://host:9100/metrics] [--record file]` | live poke with names for what the profiles know (`ifHCInOctets.3 = 812345678`), and `--record` writes the fixture `import`/`serve`/CI consume. This is the commissioning tool — and the first shot of the content capture |
| `serve --walk file \| --mockup dir \| --file metrics.txt [--listen :1161]` | stand in for the device from its recording: the in-repo SNMP agent / mockup server / metrics server, for `naut run` on a laptop and for the acceptance suite |
| `tags` | regenerate the tag file only from a committed manifest |

Recorded fixtures are ordinary text (`snmpwalk -One` numeric output, the
DMTF mockup directory layout, the raw `/metrics` body) and are committed
under `examples/it-rack/fixtures/` and `<proto>/testdata/`.

## 9. Testing

1. **Unit, no socket.** `hw/rate_test.go` (wrap, reset, first sample);
   `hw/poll_test.go` (state machine, stale-after, Enable); `hw/bind_test.go`
   (`map`/`eq`/`scale`/`const`/`derived` table); `prom/text_test.go` (parser
   golden: comments, escapes, NaN, histograms skipped); `prom/expr_test.go`;
   `redfish/path_test.go` (`[Key=Value]` selectors, missing members);
   `snmp/client_test.go` (Get/GetBulk composition through the `getter` seam).
2. **Driver against the in-repo stand-in** (`snmp/agent`, `redfish/mockup`,
   `prom/serve`, all on `127.0.0.1:0`): tags land with the right types; a
   counter step becomes the right rate; stop the stand-in → Stale +
   `__Online` false, values hold, scan never faults; restart → recovery;
   one 404/noSuchInstance leaves siblings Good; `Enable` false parks; a
   `Cmd` write reaches the wire once and read-back adopts it.
3. **Golden codegen** (`cmd/naut/{snmp,redfish,prometheus}_test.go`): fixture
   → the three files byte-for-byte; `st.Parse` + `st.Lower` the generated
   `hw_types.st`; the three importers agree on `hw_types.st` to the byte.
4. **`examples/it-rack`** (§10): `naut check` and `naut test` in CI on every
   push, against the stand-ins.
5. **Foreign implementations, in CI**, each its own job gated on an env var
   like `NAUTILUS_MODBUS_SIM`, each fixture recorded from the real thing:

   | Driver | Foreign stack | Maintenance check (do before relying on it) |
   |---|---|---|
   | snmp | **snmpsim** (`pip install snmpsim`, LeXtudio's maintained fork) replaying `.snmprec` files converted from our walks, v2c and v3 (auth+priv) | PyPI release within the last 12 months; falls back to `snmpd` from net-snmp with a `pass_persist` script if not |
   | redfish | **DMTF Redfish-Mockup-Server** serving DMTF's public mockup bundle (a legacy `Thermal`/`Power` mockup and a `ThermalSubsystem` one) | github.com/DMTF/Redfish-Mockup-Server commit within 12 months; the mockups from github.com/DMTF/Redfish-Mockups |
   | prometheus | a **real `node_exporter` release binary** scraped in CI, plus recorded fixtures for the golden tests | the profile's version pin vs latest release |

   What they must catch, mirroring what pymodbus caught for modbus:
   decode agreement on every binding kind, a v3 auth failure → `error` with
   a legible message (not a timeout), a GetBulk over a table end, a BMC
   session expiry mid-run (the mockup server can be restarted), a scrape
   with a metric renamed away (the profile's "bound metric missing" path).
6. **Real hardware, once**, and recorded (§11): `browse --record` the FS
   S3900 and mira1, `import`, `naut run`, pull a cable, watch `Down` → alarm
   → clear. Then the CyberPower PDU and UPS when the Rev E rack is bought.
   No GB hardware — those BMCs are a client production system.

## 10. `examples/it-rack` (Milestone 3)

A manifest project for the office: `SW1` (the FS S3900-24T4S-R over SNMP)
and `MIRA1` (this workstation over node_exporter), later `PDU1`/`UPS1`.
`nautilus.yaml` runs against the recorded fixtures via the stand-ins
(`naut run` works on any laptop with no hardware); `live.yaml` points the
same tags at the real devices. Alarm rules as in §6; `rack.st` derives
`RackHealth` and `PortsUp` rollups; `it-rack_test.yaml` drives the stand-ins'
values in virtual time (port goes down → alarm after 15s; fan stops → Fault;
UPS on battery). `README.md` walks the first `browse` → `import` → `run`.
This is what `spatial-hmi` renders and the phone AR session overlays.

## 11. Content capture (part of the job)

Record **before** the interesting command, every time (`content/capture.md`):
the first `naut snmp browse` of the switch; the first `naut run` with the
rack's tags on the dashboard, then in a Sparkplug host; the cable pull with
the `Down` alarm arriving (phone on the switch, screen on the alarm table);
phone photos of the switch and cabling while working. Any bug the foreign
tests catch is a war-story beat: write it down when it happens. Ideas:
**N-60** ("Your server rack is just another PLC"), part of N-59. Office and
home hardware freely; the customer cluster captured now and anonymised
before publishing (§12.3).

## 12. Risks & open questions

1. **FS S3900 vendor MIB.** IF-MIB gives ports; CPU/memory/temperature/PSU
   would need FS's private MIB (enterprise `1.3.6.1.4.1.52642`). A read-only
   walk of one S3900 on FSOS 2.2.0F found `.3507.1.1` (version string,
   serial, hardware revision), `.3507.1.2` (four integers that look like
   memory total/used/free/percent — unconfirmed) and `.9.225.1` (model,
   serial, MAC, version), and no CPU, temperature, PSU or fan objects;
   without FS's MIB pack `Switch.CpuPct/MemPct/TempC` stay 0 on that model.
   SNMP on FSOS: v3 authPriv works with SHA-256 + AES-128
   (`snmp-server view V 1.3.6.1 included` / `group G v3 priv read V` /
   `user U G v3 priv aes128 auth sha256 <priv> <auth>`, priv password
   first). Live targets: the only S3900s cabled today are the customer
   cluster's three (read-only polling, no cable pulls); spare Joy-owned
   S3900s are on the shelf for the cable-pull capture once one is racked.
2. **CyberPower MIBs.** The PDU41001 and OR1500PFCRT2U are not in the office
   yet (BOM, 09-23). Profiles are written from the published CPS-MIB and
   RFC 1628 and verified against recorded walks when the hardware arrives;
   until then their foreign tests use walks from public sources, marked so.
3. **Redfish real hardware = a customer cluster's BMCs, read-only.** The
   Rev E boards have none, so the real target is a cluster being built in
   the office: three Supermicro X14SBW-F boards with OpenBMC-based BMC
   firmware 01.06.07.00, whose Redfish trees were recorded read-only before
   power-on. Rules: **polling is read-only** — no power or reset writes,
   ever, on a customer's production system; fixtures derived from the
   recordings are **sanitised** (serials, UUIDs, MACs, hostnames, IPs and
   any customer name replaced); the live run uses the BMCs' read-only
   monitoring user, supplied as an env var from the integrator's session.
   Anything captured is anonymised before it is published, and photos of
   that rack are decided per shot under `content/sourcing.md`.
4. **BMC polling budget.** A Supermicro BMC answers `Thermal` in ~1s and
   rate-limits sessions; 10s default, one session per source, `Retries`
   cheap. Record measured RTTs per BMC family in the guide as they appear.
5. **SNMP PDU count per poll.** ~14 table columns × ceil(28/20) bulk
   requests ≈ 30 PDUs per 5s on the switch. Measure `Requests` and `RTTMs`
   on the S3900 before freezing `max-repetitions` and the default interval;
   an option to walk `ifTable` once and fan out is the fallback.
6. **Rate members on first poll** read 0.0 for one interval (§5). Accepted;
   the alternative (withholding the tag) delays `__Online` and the 3D
   view's first paint by a full interval.
7. **Redundancy.** Two replicas must not both `Set` a PDU outlet; only the
   leader's driver `Start`s, as for modbus (confirm `Coordinator` gates
   driver start, not only `WriteOutputs`; modbus §9.6 raised the same
   question).
8. **gosnmp** is the one dependency (BSD-2, `github.com/gosnmp/gosnmp`,
   verify maintenance at the fan-out). A stdlib v2c client is ~400 lines and
   trivial; v3 USM (SHA/AES key localisation, time sync) is not, and v3 is
   what a site's security review asks for. If gosnmp stalls, write v2c in
   stdlib and keep v3 behind gosnmp.
9. **`derived:` scope creep.** The fixed derivation set (`&&`, `!`, `==`,
   `/` of siblings, `Health`→`Fault`) must stay fixed; anything else is ST in
   the project. Same fence as modbus' "no expression language in bindings".
10. **Open: `Disk` UDT** (SMART via node_exporter's `smartctl_exporter`,
    Redfish `Storage`). Not in v1; the spatial session does not need it.
11. **Open: interface naming on the 3D side.** Ports are `SW1_Port01…` and
    `Switch.PortsTotal` says how many; the scene binds a `switch-port` node
    to one tag each. If the spatial session prefers to enumerate by prefix,
    `SW1_Port*` is stable too. Decide together before either side hardcodes.
