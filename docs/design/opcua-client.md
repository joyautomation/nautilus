# Design brief: OPC UA client driver for nautilus

Status: **built** — `opcua/` (manifest, client, driver, status, browse,
`codegen/`), `naut opcua import|browse|tags`, project wiring and schema.
Verified against gopcua's in-process server as a foreign implementation
(§6). Not yet run against a production server; the first intended target is
an Ignition gateway's OPC UA server.

This is the CLIENT (`driver: {type: opcua}`): nautilus reads another
server. The top-level `opcua:` section is the opposite direction, the
read-only server in `opcua/server` (guides/opcua-server.md). The two
share only the gopcua dependency.

**Goal:** a **manifest-tier OPC UA client** (`driver: {type: opcua}`) plus
`naut opcua import|browse|tags`, mirroring `eip` and `modbus` end to end. OPC
UA is what every SCADA gateway, most modern PLCs and every protocol converter
expose, so it is the bus that reaches the equipment the other two cannot —
and, read from an Ignition gateway, it is the way nautilus consumes a plant
that Ignition already polls without adding a second poller on the field
network.

## 1. The dependency decision

nautilus implements EtherNet/IP and Modbus natively. OPC UA is not: the
driver is built on `github.com/gopcua/opcua` v0.9.1 (MIT, pure Go, runtime
deps `google/uuid` and `x/exp`). The reasoning, recorded so it is not
re-litigated:

- The client surface is complete for a driver — every classic security
  policy, anonymous/username/certificate auth, Browse/Read/Write/Call,
  subscriptions — and it is the OPC UA layer inside Telegraf and benthos-umh,
  so it meets a lot of real servers.
- A native client is codec + transport + secure channel + session + services,
  a little larger than `eip/cip`, most of it crypto that must be right. That
  work buys consistency, not capability, and it can be done later behind the
  same `opcua/` package: nothing outside the package imports gopcua.
- Its weak spot — auto-reconnect state — is avoided outright (§2).
- Its server package is partial (encryption untested, several services
  unsupported) and is used only as a test fixture under the None policy. If
  nautilus ever *serves* OPC UA, that side is written natively.

v0.9.1 rather than the v0.8.0 tentacle-opcua-go pinned: the 0.9 line fixed
large encrypted reads dropping with EOF, subscription recovery, and PEM/DER
key loading, which are exactly the paths a driver hits.

## 2. Package layout & public API

```
opcua/
  manifest.go   Manifest{Namespaces, Tags}, YAML shape shared with the generator, Validate
  client.go     Dial: GetEndpoints → selectEndpoint → certificate → auth → Connect
  cert.go       self-signed application certificate with URI SAN (ported from tentacle)
  value.go      Variant ⇄ nautilus scalar; typed, range-checked writes
  driver.go     io.Driver + io.BatchReader + io.QualityReporter; one session goroutine
  status.go     Health → server.DriverStatus (internal/project/drivers.go opcuaStatus)
  browse.go     address-space walk with batched attribute reads (import/browse)
  codegen/      BrowseResult → opcua_manifest.yaml + tags/opcua.yaml
```

```go
d, err := opcua.New("opc.tcp://gw:62541/discovery", manifest,
    opcua.WithName("gateway"),                       // companion tag gateway__Online
    opcua.WithSecurity("Basic256Sha256", "SignAndEncrypt"),
    opcua.WithAuth("nautilus", os.Getenv("NAUTILUS_OPCUA_PASSWORD")),
    opcua.WithPKIDir("/var/lib/nautilus/opcua"),
    opcua.WithScanRate(time.Second), opcua.WithScanClass("slow", time.Minute),
    opcua.WithTagClass("slow", "*Performance*"))
d.Start(ctx); defer d.Stop()
```

`New` **never dials**: it validates the manifest, parses every node id, and
resolves scan classes, so `naut check` and `naut build` pass in CI with no
server in sight. The connection is `Start`'s job.

**Polled, not subscribed.** Each scan class is one batched `Read` on its
timer, chunked to the server's advertised `MaxNodesPerRead` (and halved on
`Bad_TooManyOperations`). For the hundreds of tags a gateway exposes that is
a few kilobytes a second. Subscriptions are the protocol's own change
notification and would cut it further for very large sites; they are left
out of the first cut because the polled path has no reconnect-era state to
get wrong.

**Reconnect is ours.** gopcua's `AutoReconnect` is off. A transport error
closes the session and the driver dials again on a 1 s → 60 s ladder that
resets only once the server has answered a read — modbus's rule. The quality
verdicts ride on that state, so the driver that owns the ladder owns them.

## 3. Namespaces

A node id carries a namespace *index*, and indexes are not stable: Ignition
renumbers when a tag provider is added, Kepware when a channel is. The
manifest records each binding's namespace **URI** (and the whole table at
import); at connect the driver reads the live table and rewrites the index
before the first read. A URI the server no longer has is logged and the
binding is Bad and neither read nor written until a connect finds the URI
again: its old index may now belong to another namespace holding a node of
the same id, and reading it would deliver someone else's value as Good. A
class whose bindings are all unresolvable still reads ServerStatus/State
on its schedule, so a dead session is noticed.

## 4. Quality

Per binding, from the per-node `StatusCode` of the last read:

| session | last read | quality |
|---|---|---|
| up | Good | Good (omitted) |
| up | Bad or Uncertain, or a non-scalar value | **Bad** — the session is fine, this node is not |
| up | never answered yet | NotConnected |
| up | delivered last session, not yet read on this one | **Stale** |
| down | delivered before | **Stale** — the value holds, greyed |
| down | never delivered | NotConnected |

Uncertain is deliberately not-good: showing `Uncertain_LastUsableValue` as
live is the heuristic the Quality enum exists to replace. `<name>__Online`
is the driver's own truth and always Good.

## 5. Writes

`WriteOutputs` keeps the sparkplug-host/modbus rules: gate (redundancy
leadership), baseline (the first snapshot after Start is recorded, not
sent), change (an unchanged value is not a command), last value per tag,
queued across a disconnect and flushed on reconnect — with the gate checked
again at flush, so a replica that lost leadership while down drops what it
queued (and forgets it, so the same value is a command once it leads again). Values are encoded as
the binding's declared type with a range check — a 70000 into an Int16 is
refused and logged, never wrapped. A per-node Bad write status is a refusal
on a healthy session: dropped and reported, not re-queued forever.

## 6. Testing

`opcua/driver_test.go` runs the whole driver against gopcua's `server`
package on 127.0.0.1 — a foreign implementation, with no build tags: reads,
quality (Bad node, Stale across an outage, NotConnected before delivery),
writes (baseline, change, gate, refusal of a bad value), queued writes across
a reconnect, namespace remap from a wrong index, the browse walk with real
DataType/AccessLevel attributes, endpoint selection, and the value codec.
The gopcua server routes Browse by namespace, so the fixture links its folder
under the ns=0 Objects node the way a real server would.

`cmd/naut/opcuaclient_run_test.go` runs the CLI binary end to end against
the same kind of server: `naut opcua browse`, `naut opcua import` (twice,
byte-identical), `naut check`, and `naut run` reading values, following a
change, and writing a changed output back to the one binding `--writable`
named.

Still to run: Ignition 8.1 (a trial gateway in docker, then a production
gateway), node-opcua for a second foreign implementation, and a
`Basic256Sha256 / SignAndEncrypt` session with the certificate trusted on
the server side.

## 7. Manifest, schema, project wiring

```yaml
drivers:
  - type: opcua
    name: gateway
    endpoint: opc.tcp://10.0.0.5:62541/discovery
    security: Basic256Sha256          # default: strongest offered
    security-mode: SignAndEncrypt     # default: strongest of the policy
    username: nautilus                # password from NAUTILUS_OPCUA_PASSWORD
    pki-dir: /var/lib/nautilus/opcua
    manifest: opcua_manifest.yaml     # from `naut opcua import`
    scan-rate: 2s
    scan-classes: { slow: 60s }
    tag-classes: { slow: ["*Performance*"] }
tag-files: [tags/opcua.yaml]
```

`endpoint` and `manifest` are required (schema `allOf` branch + loader).
`username` is the key sparkplug-host already has; `scan-rate`,
`scan-classes`, `tag-classes` and `manifest` are eip's. The status card is
one row per driver (one session, Kind `opcua`; the server's row is
`opcua-server`), security in the detail line, RTT and traffic Volatile.

The driver keeps the contract every protocol driver on main keeps:

- **Communications driver** (`internal/project/comms.go`): its inputs are
  seeded with `init:` or the zero of their type until first delivered, so a
  program reading them scans while the server is down. `InputType` names
  the seed type from the binding (Boolean BOOL, ≤Int32 DINT,
  UInt32/Int64/UInt64/DateTime LINT, Float REAL, Double LREAL, String
  STRING); never-delivered values report NotConnected.
- **`<driver name>__Online`**, named by `project.onlineTagFor` as eip's is
  (`opcua__Online` unnamed, `opcua-2` → `opcua_2__Online`), true once the
  session is up and every scan class has been read on it ("primed", eip's
  rule). The same verdict is the Sparkplug device's health
  (`driverHealth`), so a DBIRTH never goes out half-filled.
  `naut opcua import|tags` resolve the name from the project's
  nautilus.yaml (`project.OPCUAOnlineTag`, the resolution #459 gave eip,
  now shared) and write the companion into the tag file.
- **One Sparkplug device** per driver (a single connection), named by the
  driver.
- **Writes**: read-only by default; a binding is writable only when the
  import's `--writable` named it and the server's AccessLevel allows it.
  `SetWriteGate` is honoured, and `io.Multi` forwards it in a `drivers:`
  set.
- **`naut check`**: `New` checks the manifest, node ids and the security
  spellings offline; `Warnings()` reports a username with no
  `NAUTILUS_OPCUA_PASSWORD`, and no `pki-dir` when a secure endpoint could
  be chosen.

Not adopted from optommp/plcnext (#449): `<id>__Outages`. Those are local
I/O drivers whose programs hold outputs after an outage; no other
communications driver delivers it yet.

## 8. `naut opcua` CLI

- `import --endpoint … [--root ns=2;s=[default] --depth 4 --prefix GW_ --tags 'GW_*' --writable 'GW_*_SP']`
  browses and writes `opcua_manifest.yaml` + `tags/opcua.yaml`, sorted and
  byte-identical on re-run. Tag names are the browse path joined with `_`
  and sanitized; a collision is an error (two nodes silently sharing a tag
  is what a review must see), narrowed with `--root` or `--prefix`.
  Non-scalar types and arrays are listed as skipped with the reason.
- `browse` prints namespaces and a table of every variable.
- `tags <manifest>` re-derives the tag file offline, descriptions included.

## 9. Risks & open questions

1. **Ignition's URI SAN check.** The generated certificate carries the
   application URI as a URI SAN and announces the same URI; Ignition rejects
   a mismatch with `Bad_CertificateUriInvalid`. Untested live.
2. **Advertised vs dialled address.** The driver dials the configured URL
   even when the server advertises another (a gateway on a tailnet
   advertises its LAN address). gopcua's `SecurityFromEndpoint` takes the
   endpoint's policy and certificate, not its URL, so this holds.
3. **Arrays and structures** are skipped at import. Ignition exposes UDTs as
   folders of scalars, so this costs nothing there; a server that publishes
   structured values needs a second cut (ExtensionObject registration).
4. **Subscriptions** when a site has enough tags to justify them (§2).
5. **A native client** behind the same package if the dependency ever
   becomes a problem — the test suite is the spec for it.
6. **Server certificate trust.** gopcua v0.9.1 has no trust list, and the
   server certificate arrives in the unauthenticated GetEndpoints reply.
   The driver pins its SHA-256 thumbprint on first secured use
   (`pki-dir/trusted-servers/`) and refuses a different one; a UserName
   token is never sent on a None policy or mode, so a stripped endpoint
   list cannot downgrade the default into sending a password in the clear.
   The first connection is still trust-on-first-use.
