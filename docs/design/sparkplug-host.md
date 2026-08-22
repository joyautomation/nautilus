# Design brief: Sparkplug B Host Application driver for nautilus

One central nautilus project consumes ~60 edge-node site projects on one broker, presents them
as INPUT tags, sends operator writes back as NCMD/DCMD, publishes its own STATE, and reports
per-site comms on `/api/drivers`. Shape: a **manifest-tier `io.Driver`**
(`driver: {type: sparkplug-host}`) plus `nautilus sparkplug import`, mirroring `eip` end to end.

## 1. Package layout & public API

`sparkplug/host/` (package `host`), imported as `sphost`. Everything it needs from the parent
is already exported: `DecodePayload`, `Payload`, `Payload.Encode`, `Payload.OmitSeq`, `Metric`,
`Template`, `spb.DataType`.

Prereq rename: `sparkplug/host.go` → `sparkplug/primaryhost.go` (it is the *edge's* primary-host
STATE watcher). Pure rename.

One addition to the parent — the decode-side twin of `MetricFromValue` (payload.go:223), which
does not exist. `command.go:76`'s `var _ = ir.TypeBool // keep ir import stable for future typed
writeback` is exactly this invitation:

```go
// sparkplug/value.go
func ValueFromMetric(m Metric, t *ir.Type) (ir.Value, error)                    // scalar + Template → ir.Value
func StructDefsFromTemplates(defs []Metric) (map[string]*ir.StructDef, error)   // NBIRTH definitions → ir
```

```go
package host

type Config struct {
	BrokerURL string        // tcp://host:1883, ssl://host:8883
	HostID    string        // STATE topic spBv1.0/STATE/<HostID>
	GroupIDs  []string      // groups to consume; empty = "+" (all)
	ClientID  string        // default "nautilus-host-" + HostID
	Username, Password string
	Keepalive       time.Duration // default 30s
	Primary         bool          // publish STATE. false = passive consumer
	StateForm       string        // "3.0" (default) | "2.x" | "both"
	ReorderTimeout  time.Duration // default 5s: unfilled seq gap → NCMD Rebirth
	StaleAfter      time.Duration // 0 = off; silence this long → node stale
	RebirthOnStart  bool          // ask every manifest node to rebirth at connect (default true)
	CommandInterval time.Duration // coalesce window for outbound NCMD/DCMD (default 100ms)
	Log *slog.Logger
}

func New(m Manifest, cfg Config, opts ...Option) (*Driver, error) // NEVER dials — see §5
type Option func(*Driver)
func WithLogger(*slog.Logger) Option
func WithDiscovery(mode string) Option        // "log" (default) | "ignore" | "strict"

func (d *Driver) ReadInputs() (nio.Values, error)     // io.Driver
func (d *Driver) WriteOutputs(nio.Values) error
func (d *Driver) Start(ctx context.Context)           // matches runcmd.go:87's assertion
func (d *Driver) Stop()                               // needs a NEW assertion in runcmd (§5)
func (d *Driver) InputNames() []string                // mirrors eip.Driver
func (d *Driver) OutputNames() []string
func (d *Driver) StructDefs() map[string]*ir.StructDef
func (d *Driver) Status() Status
func (d *Driver) Discovered() []Discovered            // metrics seen but not in the manifest
func (d *Driver) RequestRebirth(node string) error

type Status struct {
	Broker, HostID string; Groups []string
	Connected bool; StateOnlineMs int64
	Msgs, Rebirths, SeqGaps, WriteDrops uint64
	Unknown int; LastError string
	Nodes []NodeStatus
}
type NodeStatus struct {
	Group, EdgeNode string; Online, Stale bool
	BdSeq int64; Seq uint64; BirthMs, LastMsgMs int64; Metrics int
	Devices []DeviceStatus
}
type DeviceStatus struct{ ID string; Online bool; BirthMs int64; Metrics int }
```

Internal seam — what makes it testable and parallelizable:

```go
type publisher interface{ publish(topic string, qos byte, retain bool, payload []byte) error }
func (d *Driver) handleMessage(topic string, payload []byte) // pure; no MQTT types
```

## 2. MQTT topology & state machine

**Subscriptions**, all issued before the STATE birth publish:

| Topic | QoS | Why |
|---|---|---|
| `spBv1.0/STATE/<host_id>` — **literal, no wildcard** | 1 | TCK `payloads-state-subscribe` and `message-flow-phid-sparkplug-subscription` compare the SUBSCRIBE filter by **exact string**; `spBv1.0/STATE/#` fails them |
| `spBv1.0/<group>/NBIRTH/+`, `NDEATH/+`, `NDATA/+` | 1 | node traffic |
| `spBv1.0/<group>/DBIRTH/+/+`, `DDEATH/+/+`, `DDATA/+/+` | 1 | device traffic |

Six filters per group, preferred over `spBv1.0/<group>/#`, which would echo our own NCMD/DCMD
back and pull in other hosts' retained STATE.

**Connection** — mirror `node.go:176-199` with two deliberate divergences:

- `SetCleanSession(true)`, `SetOrderMatters(true)` (mandatory — seq tracking depends on arrival
  order), `SetConnectTimeout(30s)`.
- `SetBinaryWill("spBv1.0/STATE/"+HostID, willJSON, 1, true)` — **QoS 1, retain true**. The
  edge's NDEATH is retain=false; the host's will is not.
- `willJSON = {"online":false,"timestamp":ts}` with `ts := time.Now().UnixMilli()` captured
  **once per session before CONNECT**; the STATE birth must carry the byte-identical value
  (TCK `host-topic-phid-birth-payload-timestamp` compares `==`; `...death-payload-timestamp-
  connect` also wants it within 5 s of CONNECT).
- **`SetAutoReconnect(false)`.** Paho bakes the will into CONNECT and reuses it across
  auto-reconnects, so a long-lived client's will timestamp goes stale and diverges from a fresh
  birth. Own the reconnect loop (capped backoff 1 s → 30 s, shape of `eip/driver.go:400-438`),
  rebuilding the client — and the will timestamp — per attempt.

On connect: subscribe all → publish STATE birth `{"online":true,"timestamp":ts}` QoS 1 retain
**true** (must be the first PUBLISH after CONNECT) → if `RebirthOnStart`, NCMD Rebirth to every
manifest node, **staggered ~50 ms** so 60 sites don't birth at once.

`Stop()`: publish STATE death `{"online":false,"timestamp":now}` QoS 1 retain true, `.Wait()`
the token, **then** `Disconnect(250)`. The TCK requires the host's own death publish before both
clean and unclean teardown — the broker firing the LWT is explicitly not a substitute.

`StateForm: "both"` additionally publishes retained `ONLINE`/`OFFLINE` on `STATE/<host_id>` (no
prefix); nautilus's own edge subscribes to both forms (`primaryhost.go:33-34`).

**Per-node state:**

```go
type nodeState struct {
	group, edge string
	online   bool
	bdSeq    int64
	seq      uint64; seqPrimed bool
	birthMs, lastMs int64
	aliases  map[uint64]metricRef            // ONE table per edge node, shared with its devices
	devices  map[string]*deviceState
	tmpl     map[string]*sparkplug.Template  // NBIRTH template definitions
	gapTimer *time.Timer
	pending  map[uint64]queued               // out-of-order buffer, keyed by seq
}
```

| Message | Handling |
|---|---|
| **NBIRTH** | `seq` MUST be 0 → reset state, read `bdSeq`, build alias table from metrics carrying both name and alias, capture `IsDefinition` Templates, mark online + `birthMs`, apply values. Manifest metrics absent from the birth → stale. nautilus's own edge sends full names and no aliases (`birth.go:23-29`), so handle both. |
| **DBIRTH** | same per device; consumes a seq. Aliases go in the node's single table (spec: unique within the edge node incl. devices). |
| **NDATA / DDATA** | require `seq == (last+1)%256`. Match → apply. Gap → buffer + start `ReorderTimeout`; missing seq arrives first → drain in order, cancel timer; timer fires → `RequestRebirth`, drop buffer, `SeqGaps++`. TCK's harness window is 30 s, so 5 s is comfortably inside. |
| **NDEATH** | payload carries only `bdSeq`. **If it doesn't match the node's current bdSeq, ignore** — a late will from a prior session. Match → node offline, every device offline, all tags stale. |
| **DDEATH** | device offline, its tags stale; consumes a seq. |
| **STATE** (our own echo) | ignore; the subscription exists only to satisfy the spec. |

**Tag values on death — the important call. Keep the last value; never zero, never delete.**
That is Sparkplug's own semantics (the last value *is* the value; quality is separate) and it
matches the runtime, which holds last-known values when a driver read fails. Quality rides on
driver-synthesized companion tags, declared in the manifest like any other so ST can interlock
and the HMI can bind:

- `<site>__Online` — BOOL, node online (NBIRTH seen, no matching NDEATH)
- `<site>_<device>__Online` — BOOL, per device
- `<site>__LastBirthMs` — INT (epoch ms)
- `<site>__Rebirth` — BOOL **output**; rising edge → NCMD Rebirth (operator-forced)

Double underscore is the reserved marker: the sanitizer (§3) collapses `_` runs, so no real
metric name can produce it.

`ReadInputs` **never errors once `Start` has been called** — it returns the whole snapshot,
offline nodes included, with `__Online=false`. Data metrics stay *absent* from the map until
first seen, which preserves "reads fault": a site that has never birthed faults the scan that
reads it, correctly and loudly, while `__Online` is present from t=0 so alarm logic works before
the first birth. Before `Start` it errors like eip (`host: not connected yet`).

**TCK `host-application` profile** (85 IDs, 14 scenarios) maps 1:1 onto the above:
`HostCONNECTHasWill`/`HostWillCompliant` (will topic, QoS 1, retain true, `{online:false,ts}`);
`HostWillTimestampIsRecent` (ts within 5 s of CONNECT → own reconnect loop); `HostCleanSession`;
`HostSTATEBirthAfterSubscribe`/`HostBirthCompliant` (literal STATE subscribe before first
publish); `HostBirthTimestampMatchesWill` (one `ts` for will + birth);
`HostDeathBefore{Clean,Unclean}Disconnect` (`Stop()`); `HostN/DCMDCompliant` (QoS 0, retain
false, timestamp, named metrics, Rebirth = Boolean(11) `true`); `HostMessageOrdering` (reorder
buffer → NCMD Rebirth); `HostND/DDEATHActions`. The profile is gated on a CONNECT whose will
topic starts with `spBv1.0/STATE/`, and an all-N/A run still exits 0 — assert a non-zero PASS count.

## 3. Tag mapping

**Sanitizer**, mirroring `eip/codegen/codegen.go:247-266` `tagIdent`: replace every rune outside
`[A-Za-z0-9_]` with `_`, trim leading/trailing `_`, collapse `__`→`_`, prefix `M_` if empty or
digit-initial. Collisions resolved by the same `uniqueName` (`_2`, `_3`) at generation time;
`composeTags` (project.go:365) rejects duplicates outright regardless.

**Name** = `<prefix>_<sanitize(device)>_<sanitize(metric)>`, prefix defaulting to
`sanitize(edge_node_id)`, overridable per node; node-level metrics omit the device segment.
`W6` + `Well/Level` → `W6_Well_Level`; `W6`/`PLC1` + `Pump/Run` → `W6_PLC1_Pump_Run`.

**Two layouts, a generator flag.** `--layout flat` (**default, recommended**): one tag per
metric, so a partial NDATA update is a single-tag write and RBE globs (`W6_*`), the historian,
and the HMI all work per tag. `--layout struct` (`W6.Well.Level` in ST, one TYPE per site) is
rejected as default because every partial metric update becomes a read-modify-write of the site.

**Templates/UDTs always become ST TYPEs**, independent of layout — the treatment
`nautilus eip import` gives Logix UDTs. NBIRTH `IsDefinition` templates are harvested into
`sparkplug_types.st` and `types:`; an instance metric becomes one tag with `type: <TemplateRef>`.
`DecodePayload` already yields `*Template` with nested `Metric`s; match member names against
`StructDef.FieldIndex`, keep the previous value for absent members (partial template updates are
legal), log-once and ignore unknown members.

**Static vs dynamic.** The runtime's tag set is fixed at compose time, so the manifest is the
only path — nothing is auto-created. Unmanifested metrics go through `on-unknown:` — `log`
(default: logged once each at INFO with the exact YAML line to add, counted, exposed via
`Driver.Discovered()` and `/api/drivers` `extra.unknown`), `ignore` (counted only), or `strict`
(WARN + state `degraded`). Never silently dropped; the remedy is re-run the import, review the diff.

## 4. Writes: output tags → DCMD/NCMD

`runtime.go:590-608` hands `WriteOutputs` **all** outputs **every scan**, rebuilt from scratch —
change detection is the driver's job. Port `eip/driver.go:364-395` verbatim: filter to `byName`,
suppress against both `written` and `pending` via `equalValue`, non-blockingly kick `wkick`; a
writer goroutine drains on a `CommandInterval` tick.

Destination comes straight from the binding (`node` / `device` / `metric`):
`device: ""` → `spBv1.0/<group>/NCMD/<edge>`; otherwise → `spBv1.0/<group>/DCMD/<edge>/<device>`.
QoS **0**, retain **false**, `Payload{Timestamp: nowMs, OmitSeq: true, Metrics: [...]}` —
`OmitSeq` already exists (payload.go:44). Always the full metric **name**, never an alias. All
changed metrics for one destination coalesce into one payload per flush.

`Node Control/Rebirth` is reserved: a manifest binding using that metric name is rejected in
`New()`; `<site>__Rebirth` is the sanctioned operator path.

Writes to an offline node are **dropped and counted** (`Status.WriteDrops`), not queued — a
command to a dark site is meaningless and the operator should be seeing `__Online=false`.

Writable bindings generate as `role: output` with an `init:` (the manifest's, else the type
zero) so the tag exists from scan one; `RoleOutput` is unseeded by default
(`runtime/tagdef.go:119-122`) and an unwritten output is simply omitted from the Values map.

## 5. Manifest, schema, project wiring

```yaml
# nautilus.yaml
driver:
  type: sparkplug-host
  broker: "tcp://mqtt.pomona:1883"
  group-id: PomonaWRD
  host-id: pomona-central          # STATE topic spBv1.0/STATE/pomona-central
  manifest: sparkplug_manifest.yaml
  primary: true
  state-form: both                 # 3.0 JSON + legacy STATE/<host> for older edges
  reorder-timeout: 5s
  stale-after: 2m
  on-unknown: log
tag-files: [tags/sparkplug.yaml]
```
Password from `NAUTILUS_MQTT_PASSWORD`, never in the file (the rule `sparkplug:` already sets).

`sparkplug_manifest.yaml` (generated) follows `eip.Manifest`'s convention exactly — **no yaml
struct tags**, so keys are lowercased Go field names (`arraylen`, `edgenode`), decoded with
`KnownFields(true)`. Shown here in flow style for brevity; the generator emits yaml.v3 block
style like `eip_manifest.yaml`:

```yaml
# Generated by `nautilus sparkplug import --broker tcp://mqtt.pomona:1883 --group PomonaWRD`
# Re-run the import to refresh; births are validated against it at runtime.
group: PomonaWRD
types:
    - name: Motor
      fields:
        - { name: Speed, type: Double,  arraylen: 0 }
        - { name: Run,   type: Boolean, arraylen: 0 }
nodes:
    - edgenode: W6
      prefix: W6
      onlinetag: W6__Online
      birthtag: W6__LastBirthMs
      rebirthtag: W6__Rebirth
      devices:
        - { device: PLC1, onlinetag: W6_PLC1__Online }
tags:
    # name = nautilus tag; device "" = node-level (NDATA); metric = verbatim
    # Sparkplug name; type = a Sparkplug datatype name or a types: name.
    - { name: W6_Well_Level,        node: W6, device: "",   metric: Well/Level,    type: Double,  arraylen: 0, writable: false }
    - { name: W6_Pump1,             node: W6, device: "",   metric: Pump1,         type: Motor,   arraylen: 0, writable: false }  # Template → ST struct
    - { name: W6_PLC1_Pump_Run,     node: W6, device: PLC1, metric: Pump/Run,      type: Boolean, arraylen: 0, writable: false }
    - { name: W6_PLC1_Pump_SpeedSP, node: W6, device: PLC1, metric: Pump/SpeedSP,  type: Double,  arraylen: 0, writable: true, init: 0.0 }
      # writable → spBv1.0/PomonaWRD/DCMD/W6/PLC1
```

`Manifest.structDefs()` is `eip/manifest.go:78-140` adapted — same memoized cycle-detecting
build, Sparkplug datatype names instead of Logix (`Boolean→ir.BoolT`; `Int8..UInt64,DateTime→
ir.IntT`; `Float,Double→ir.RealT`; `String,Text,UUID→ir.StringT`; else recurse as struct).

`tags/sparkplug.yaml` is rendered by the **existing shared** `internal/tagfile.Render` — the same
helper `nautilus eip import` and `nautilus tags import-csv` use, so sorting, duplicate rejection,
and float formatting come free:

```yaml
# Generated by `nautilus sparkplug import` from births on tcp://mqtt.pomona:1883
# (group PomonaWRD, 2 nodes, 61 metrics). Do not edit — re-run the import.
- { name: W6__LastBirthMs, role: input }
- { name: W6__Online, role: input }
- { name: W6__Rebirth, role: output, init: false }
- { name: W6_PLC1__Online, role: input }
- { name: W6_PLC1_Pump_SpeedSP, role: output, init: 0.0 }
- { name: W6_Pump1, role: input, type: Motor }
- { name: W6_Well_Level, role: input }
```

**CLI.** `nautilus sparkplug import | browse | tags`. Import flags: `--broker` (req),
`--group` (req, repeatable), `--host-id` (default `nautilus-import`),
`--nodes`/`--metrics`/`--writable` globs, `--listen 30s`, `--rebirth` (default true),
`--sites <file>` (offline generation from a site list), `--layout flat|struct`,
`--prefix node|none|<literal>`, `--out .`, `--tags-out tags/sparkplug.yaml`, `--tags-skip`.
Writes `sparkplug_types.st`, `sparkplug_manifest.yaml`, `tags/sparkplug.yaml` — wholesale
overwrite, deterministic sorted output so regeneration diffs cleanly. Split the code the way
`eip/codegen` is: a network-dependent `Generate(births, Options)` and a **pure**
`TagsYAML(manifest, ...)`, so `nautilus sparkplug tags` needs only the repo. Follow eip's
fail-loud rule — a `--tags-skip` pattern matching nothing is an error. Discovery reuses the
driver's own decoder, so the generator cannot drift from the runtime.

**Code changes:**

1. `internal/project/project.go` — new `DriverConfig` fields (`broker`, `group-id`, `group-ids`,
   `host-id`, `client-id`, `username`, `primary`, `state-form`, `reorder-timeout`,
   `stale-after`, `on-unknown`, `rebirth-on-start`); reuse the existing `manifest` key. New
   `case "sparkplug-host":` in `buildDriver` reading the manifest through `fsys` with
   `KnownFields(true)`, exactly like the eip case (project.go:630-640). **Extend the `default:`
   message but keep the substring `manifest projects support`** — both `schema_test.go:150` and
   `project_test.go:119` match on it.
2. **`New()` must not dial.** `buildDriver` runs inside `nautilus check` (check.go:210) and
   `nautilus build`, i.e. in CI with no broker. Construct offline, connect in `Start` — the eip
   split.
3. `internal/project/drivers.go` — add `hostDrv, hasHost := p.Runtime.Driver.(*sphost.Driver)`
   to `DriverStatus` (the eip assertion at line 17 is concrete; there is no generic seam) and a
   `hostStatus(sphost.Status) server.DriverStatus` adapter beside `eipStatus`.
4. `cmd/nautilus/runcmd.go` — after line 89, add
   `if s, ok := proj.Runtime.Driver.(interface{ Stop() }); ok { defer s.Stop() }`. Nothing calls
   a driver's `Stop()` today; the STATE death publish requires it. Harmless for eip, which has one.
5. `tools/vscode-iec/schemas/nautilus.schema.json` — add `"sparkplug-host"` to
   `definitions.driver.properties.type.enum`, one property per new Go field
   (`TestSchemaMatchesLoader` at schema_test.go:117 does an exact key-set `reflect.DeepEqual`),
   and an `allOf/if type==sparkplug-host then required:[broker, host-id, manifest]` branch.
6. `internal/project/schema_test.go` — `TestSchemaDriverTypesAreImplemented` needs no edit (it
   calls `buildDriver` with an empty config and tolerates any error but the unknown-type one).
   Add a required-fields case to `TestSchemaRequiresWhatLoaderRequires` mirroring eip's at line 197.
7. `cmd/nautilus/main.go` — `case "sparkplug":` + usage line.

**`/api/drivers` shape.** `Kind: "sparkplug-host"`, `Name: <host-id>`,
`Detail: "<broker> · <group>"`. State: `error` (broker down) → `connecting` (connected, no
births) → `degraded` (any node offline, or unknown metrics in strict mode) → `connected`.
Metrics: `sites online` (`52 / 60`), `messages`, `rebirths`, `seq gaps`, `unknown metrics`,
`last message` (via the existing `agoText`). **`Devices []DriverDevice` is the per-site row** —
one entry per edge node (`{ID: "W6", Online: true, Detail: "12 tags · born 3m"}`), device
sub-rows flattened as `W6/PLC1`. That satisfies the HMI comms-status requirement with **zero HMI
changes**: `DriverStatusPanel.svelte` / `DriverStatusCard.svelte` already render `Devices`.
`Extra: {"nodes": [...NodeStatus...]}` for a future richer page.

## 6. Testing

Today `sparkplug/conformance_test.go` shells out to
`go run .../cmd/sparkplug-tck@v0.1.2 -harness -profile edge-node`, gated on `NAUTILUS_TCK=1`;
the TCK embeds its own broker (mochi v2.7.9). No docker. `eip/driver_test.go` uses the in-repo
`eip/logixserver` emulator with **no build tags and no env gating**.

1. **Unit, no broker.** `handleMessage(topic, payload)` is pure, so births/data/deaths are fed as
   bytes built with `sparkplug.Payload{...}.Encode()`. Covers alias tables, seq gaps and the
   reorder buffer, stale-bdSeq NDEATH, template assembly into `ir.Value`, partial template
   updates, and a table test for the sanitizer + collisions. Most of the value, zero cost.
2. **In-process broker acceptance.** Add `github.com/mochi-mqtt/server/v2` as a **test-only**
   dep — the TCK already pins v2.7.9, so it is known-good. Start it on `127.0.0.1:0`, and **use
   the real `sparkplug.Node` from the parent package as the fake edge** (edge↔host dogfooded in
   one test), plus a raw paho publisher for what the real node never emits: seq gaps, alias-only
   data, stale NDEATH, partial templates. Assert tags land, NDEATH holds values and clears
   `__Online`, a gap produces `spBv1.0/G/NCMD/W6`, an output write produces a DCMD.
3. **TCK host profile**, mirroring conformance_test.go: `-harness -profile host-application`,
   gated `NAUTILUS_TCK=1`. Two facts to design around: `internal/harness` is under `internal/`
   and **cannot be imported** (shell out, parse the `-json`
   `[]{assertion_id, subject, status, detail}`, as the existing test does); and **the harness
   does not simulate an edge node** (`-rebirth` targets an edge SUT), so without traffic
   `HostMessageOrdering`, `HostND/DDEATHActions`, and `HostN/DCMDCompliant` all go N/A and the
   run still exits 0. The test must drive its own edge client into the harness broker (NBIRTH
   seq=0, DBIRTH, NDATA, a deliberate gap, DDEATH, NDEATH) **and assert a non-zero PASS count**.
4. **Golden-file generator test** (`cmd/nautilus/sparkplug_test.go`): recorded NBIRTH payloads in
   `testdata/`, all three generated files asserted byte-for-byte, and — copying
   `eip/codegen/codegen_test.go` — actually `st.Parse` + `st.Lower` the generated
   `sparkplug_types.st`, so a broken import fails in the test rather than on disk.
5. **`examples/pomona-host/`** with committed generated files, covered by
   `cmd/nautilus/check_manifest_test.go` — the shape regression-tested without a broker.

## 7. Implementation plan

| # | Work | Effort | Parallel? |
|---|---|---|---|
| **A1** | Rename `host.go`→`primaryhost.go`; add `ValueFromMetric` + `StructDefsFromTemplates`; **fix the `publisher`/`handleMessage` seam in this PR** | 0.5 d | blocks all |
| **B1** | `host/manifest.go` + `mapping.go`: manifest structs, `structDefs()`, sanitizer, `InputNames`/`OutputNames`. No MQTT | 1 d | ✅ B2/B3 |
| **B2** | `host/state.go`: the state machine — aliases, births, seq/reorder, deaths, templates, snapshot. No MQTT | 2 d | ✅ |
| **B3** | `host/mqtt.go` + `status.go`: Config, New, Start/Stop, own reconnect loop, subscriptions, STATE/LWT, NCMD/DCMD writer, `Status()` | 1.5 d | ✅ |
| **C1** | Manifest tier: `project.go`, `drivers.go`, JSON schema, `schema_test`, `runcmd.go` `Stop()` | 1 d | ✅ C2 |
| **C2** | `cmd/nautilus/sparkplug.go` + `host/codegen`: import/browse/tags, `sparkplug_types.st`, golden tests | 1.5 d | ✅ |
| **D1** | mochi acceptance test with a real `sparkplug.Node` as the fake edge | 1 d | ✅ D3 |
| **D2** | TCK `host-application` conformance + CI; fix what it flags | 1 d + remediation | before D3 |
| **D3** | `examples/pomona-host/` + manifest-first guide page | 1 d | ✅ |

≈10–11 engineer-days; ≈5–6 wall-clock with 3 agents on B and 2 on C.

**Safe to parallelize:** {B1, B2, B3} touch disjoint files in a new package *once A1 fixes the
seam*; {C1, C2} are disjoint (C1: `internal/project` + schema + runcmd; C2: `cmd/nautilus` + a
new codegen dir); {D1, D3}.
**Not parallelizable:** A1 (everyone imports it); anything touching `internal/project/project.go`
(C1 alone — one `switch`, one struct); D2 before D3, since TCK findings will change B3.

## 8. Risks & open questions

1. **Redundancy interaction — highest risk.** With `redundancy:` (lease election) a standby
   replica must **not** publish STATE online, or its LWT flaps store-and-forward across all 60
   sites. Gate the STATE publish (and outbound commands) on `runtime.Coordinator`/leadership.
   Design into B3; do not retrofit.
2. **Paho auto-reconnect vs. the STATE will timestamp.** The will is baked into CONNECT, but the
   TCK wants birth-ts == will-ts and will-ts within 5 s of connect. Mitigated by owning the
   reconnect loop — the easiest thing to get wrong, and the reason B3 must not just copy
   `node.go`'s options block.
3. **Alias scope.** Spec: unique within an edge node including its devices; some implementations
   scope per device. Use one table per node, fall back to per-device on collision, log it. Mostly
   theoretical — Ignition and nautilus's edge both send full names.
4. **Scale.** 60 sites × ~100 metrics ≈ 6 000 tags; `Tags.Snapshot()` copies the whole map per
   scan and the SSE frame and historian carry all of it. Recommend a 500 ms–1 s scan on the
   central project and running `runtime/scan_bench_test.go` at 6 000 tags before committing.
5. **Reads fault on never-delivered metrics** — intentional, but one site that has never birthed
   faults every scan of any task reading it. Keep site logic in per-site tasks (one dead site
   faults one task) and guard on `__Online`. Document loudly.
6. **Retained births are forbidden by spec**, so a host starting mid-stream sees nothing until it
   asks. `rebirth-on-start: true` is the fix; stagger the 60 requests.
7. **Open: how does `sparkplug_types.st` get compiled?** `eip_types.st` is picked up because
   `project.go:415` treats every root-level `.st` without a `PROGRAM` as a library. Confirm the
   generated file lands in the project root (as `examples/client60` does), not a subdirectory, or
   `type:` references won't resolve.
8. **Open: host-as-edge.** A project with both `driver: {type: sparkplug-host}` and a
   `sparkplug:` section would consume the group and republish it as one giant edge node (cloud
   aggregation). Legal and maybe desirable, but `nautilus check` should warn when
   `driver.host-id == sparkplug.primary-host`.
9. **Integer width.** nautilus's IR collapses all integers to int64, so a `UInt64` metric above
   2^63 wraps. The edge side already has this; document it.
