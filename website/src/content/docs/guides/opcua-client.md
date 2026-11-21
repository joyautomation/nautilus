---
title: Reading OPC UA (preview)
description: Poll an OPC UA server (an Ignition gateway, a PLC's embedded server, a Kepware) from a committed manifest, with per-tag quality, typed writes you opt into per tag, and namespace-safe node ids.
---

:::caution[Preview]
Tested against gopcua's in-process server, open62541, Eclipse Milo,
Ignition 8.3 and CODESYS Control Win V3 (see
[Servers it has been run against](#servers-it-has-been-run-against)), each
over `Basic256Sha256 / SignAndEncrypt`, and against virtual IO-Link
masters. It has not yet been run against Ignition 8.1, Kepware or a PLC's
own embedded server. Try it against your server before you depend on it.
:::

A `driver: {type: opcua-client}` section makes nautilus an OPC UA **client**: it
polls one server's variables into the tag store. This is the opposite
direction to the [`opcua:` section](/guides/opcua-server/), which *serves*
this project's own tags to OPC UA clients; a project can do both.
Everything the driver polls comes from a browse of the server that
`naut opcua import` turns into a committed manifest and a committed tag
file, the way `naut eip import` and `naut modbus import` do.

```yaml
driver:
  type: opcua-client
  endpoint: opc.tcp://192.0.2.10:4840
  security: Basic256Sha256        # default: the strongest the server offers
  security-mode: SignAndEncrypt   # default: the strongest mode of that policy
  username: nautilus              # password from NAUTILUS_OPCUA_PASSWORD
  pki-dir: /var/lib/nautilus/opcua
  manifest: opcua_manifest.yaml   # from `naut opcua import`
  scan-rate: 1s
  scan-classes: { slow: 60s }
  tag-classes: { slow: ["*Diagnostics*"] }
tag-files: [tags/opcua.yaml]
```

`endpoint:` and `manifest:` are required; a project missing either fails
`naut check`, not `naut run`. The driver never dials while it is being
built: it validates the manifest, parses every node id and checks the
`security:` and `security-mode:` spellings offline, so `naut check` and
`naut build` pass in CI with no server in sight. A `username:` with
`security: None` or `security-mode: None` fails `naut check` (see
[Security](#security)). `naut check` warns when `username:` is set and
`NAUTILUS_OPCUA_PASSWORD` is empty, when `username:` is set without
`security-mode: Sign` or `SignAndEncrypt`, and when there is no `pki-dir:`
while the security settings allow an endpoint that needs a certificate. Several servers are several entries in a `drivers:` list,
each with its own `name:`.

## Generating the manifest: `naut opcua import`

```sh
naut opcua import --endpoint opc.tcp://192.0.2.10:4840 \
  --root 'ns=2;s=[default]' --prefix GW_ --writable 'GW_*_SP'
```

That browses the address space under `--root` (default: the Objects
folder) and writes `opcua_manifest.yaml` and `tags/opcua.yaml`. Both are
generated, never hand-edited, sorted, and byte-identical on re-run against
an unchanged server. Tag names are the browse path joined with `_`
(`Plant/Pump1/Speed` → `Plant_Pump1_Speed`), sanitized to an identifier.
Two nodes that would share a name are an error, not a suffix: narrow
`--root` or run one import per `--prefix`.

A variable whose DataType is a subtype of a scalar built-in is imported
as the built-in its value is encoded as: an enumeration (any subtype of
`Enumeration`, such as a nautilus server's `ns=1;s=type:Mode`) as `Int32`,
the member's value, and a `Duration` as a `Double` of milliseconds.
Variables whose type is not a scalar built-in (structures) and arrays are
listed as skipped, with the reason. `--tags` keeps a subset
by glob.

The tag file also declares the driver's `<name>__Online` companion. Its
name comes from `--name` when given, else from the `opcua-client` driver in the
project's `nautilus.yaml` whose `endpoint:` matches (then whose `manifest:`
matches, then the only one), else `opcua_client__Online`. The import prints which.

`naut opcua browse --endpoint …` prints the namespaces and a table of every
variable: path, node id, type, access, description. `naut opcua tags
opcua_manifest.yaml` re-derives the tag file from the committed manifest
without a server. None of the three commands writes to the server.

## Namespaces survive a renumbering

A node id carries a namespace *index* (`ns=2;s=…`), and indexes are not
stable: a server may renumber when a provider or channel is added. The
manifest records each binding's namespace **URI**; at connect the driver
reads the server's live namespace table and rewrites the index before the
first read. A binding whose URI the server no longer has is **Bad** and is
not read or written at all (its old index may now belong to another
namespace with a node of the same id), and the log says why. It reads
again once a connect finds the URI.

## Polling or subscribing, quality and `__Online`

By default each scan class is one batched Read on its own timer, chunked
to the server's advertised `MaxNodesPerRead` (halved if the server answers
`Bad_TooManyOperations`).

With `read-mode: subscribe` each scan class is instead one OPC UA
subscription: its publishing interval is the class rate, and the server
sends a value only when it changes. That cuts traffic and server load when
most values sit still, which is most of a plant most of the time. Use it
when a server has many tags or many clients. Polling is the
simpler path, and the right one for a handful of tags.

```yaml
driver:
  type: opcua-client
  endpoint: opc.tcp://192.0.2.10:4840
  manifest: opcua_manifest.yaml
  read-mode: subscribe     # poll (default) | subscribe
  scan-rate: 200ms         # the default class's publishing interval
```

Everything below holds in both modes. A subscribed session is first read
once per class, so `__Online` and quality mean the same thing; then it is
checked once a second, because a subscription on its own does not tell a
client that its server has gone. A restart is noticed within that second,
a server that goes silent after one request timeout. Each class is also
read in full every 30 s (every ten periods for a slower class), so a
notification lost on the way costs at most that long. A class slower
than a quarter of the request timeout is still sampled at its own rate;
the server just publishes more often than that, so its keep-alive stays
well inside the timeout. Subscriptions are
created fresh on every new session. A server may publish slower than the
class asks; the driver logs it and the status card shows the interval the
server granted. If the server refuses a subscription the session fails
and is retried with backoff, and the error says why: the driver never
falls back to polling on its own; the retry delay climbs until a
session subscribes. A node the server will not monitor reads **Bad** for
the whole session, refresh reads included, since nothing follows it in
between. If the server refuses a call creating too many monitored items
at once (`Bad_TooManyOperations`), the driver halves the batch and keeps
the smaller size for later sessions.

- `<name>__Online` (BOOL) is true once the session is up and every scan
  class has been read on it, false otherwise. Its name follows the driver's
  `name:` (`opcua_client__Online` for an unnamed one; a second unnamed
  one, `opcua-client-2`, gets `opcua_client_2__Online`).
- A node the server refuses (unknown id, wrong namespace, a dead device
  behind a gateway, an array) is **Bad** while the rest of the class keeps
  delivering. `Uncertain` statuses count as not good, on purpose, with one
  exception: `Uncertain_SubstituteValue` (a value the server itself holds in
  place of the real one) keeps its value and reads **Substituted**.
- A transport failure reconnects with backoff (1 s doubling to 60 s,
  reset once the server answers a read). Values hold, their quality goes
  **Stale**, and `__Online` goes false. After a reconnect each value stays
  Stale until the new session has read it.
- Before the first delivery an input reads as its `init:` or the zero of
  its type, and its quality is **NotConnected**. A program that reads a
  server's tags keeps scanning while the server is down, and checks
  `__Online` before trusting them, as with the other protocol drivers (see
  the [tag model](/guides/tag-model/)). The type comes from the binding:
  Boolean is BOOL, integers up to Int32 are DINT, UInt32, Int64, UInt64 and
  DateTime (delivered as Unix milliseconds) are LINT, Float is REAL, Double
  is LREAL, String is STRING.

## Writes are opt-in, per tag

A binding is read-only unless the import marked it writable: `--writable`
takes globs, defaults to none, and never marks a node the server itself
says is read-only. A writable binding becomes an `output` in the tag file.

Writes are typed by the binding. A value that does not fit an `Int16` is
refused and logged, never wrapped. They follow the rules every nautilus
driver keeps: an unchanged value is not a command; a command made while the session is
down goes out on reconnect (the last value per tag); a write the server
refuses on a healthy session is dropped and reported, not retried forever.
"Unchanged" at start means unchanged against what the server holds. The
first time a scan that has read the server's inputs hands the driver the
program's value for an output, the driver compares it with the server's:
equal, nothing is sent; different, the program's value is written once. The
very first scan runs before anything was read, on the inputs' initial
values, and its values are never sent. An output computed from another
device's input is not waited for: interlock on that device's `__Online`. A controller that computes `true` at
start therefore drives a server that holds `false`, and a program that
starts at its default overwrites a value left on the server. The output
is the controller's to own: a value an operator sets on the server belongs
in an input (a setpoint the program reads), not in an output. An output
whose node has not been read yet waits for the first read; one the server
will not let it read, or that has no scan class, is written once.
Earlier versions took the first value as already sent and never wrote it
(#1121).

Under `redundancy:` only the leader writes, and leadership is checked
again when queued commands go out, and before each chunk of a large batch:
a replica that lost it during an outage drops what it queued, and one
demoted while a batch is going out sends no more of it, and logs how many
writes went out and the names of the tags it dropped. When the write gate reopens, every output goes out
once more with the program's value, changed or not, since another
replica may have written the nodes in between, and a replica that started
as a standby has no start-up baseline, so its first push as leader
commands. The driver sees the gate by looking at it, at least once a
second while connected and on every connect attempt: a flip between two
looks is not seen.

The same happens when a lost session is re-established: every output is
commanded once more, changed or not, because a server that restarted with
its state reset holds its start-up values, and an unchanged value would
otherwise never be written again. It goes through the write gate, so on a
pair only the leader sends it. `reassert-on-reconnect: false` turns it
off, for a server where a repeated write has a side effect.

## Security

`security:` names the policy, `security-mode:` the mode. Leave both out and
the driver takes the strongest endpoint the server offers. Any policy but
`None` needs a client certificate, generated self-signed into `pki-dir` on
first use, with the application URI as a URI SAN. The server must trust it
(on Ignition: *Config > OPC UA > Security > Trusted Certificates*; on
another nautilus controller: `naut opcua trust <thumbprint>` there, see
[Approving a client](/guides/opcua-server/#approving-a-client)). A nautilus
server offers secure endpoints too, so a client left on the default picks
one of them, and its first connection is refused until that server trusts
its certificate. Some servers refuse a certificate they do not trust by
closing the connection mid-handshake, with no status code; the driver's
error then says so and names the certificate file to trust. Set
`security: None` only where an unsecured, read-only link is what you want. The
password for `username:` is never in the manifest: set
`NAUTILUS_OPCUA_PASSWORD`.

Each generated certificate has a subject of its own
(`CN=nautilus@<host>` plus a random serial number) and an Authority Key
Identifier naming its own key. On a CODESYS runtime, of several trusted
client certificates that shared one subject, only one was accepted:
trusting the second broke the first. Earlier releases gave every
certificate the subject `CN=nautilus`, and an existing certificate is kept
as it is. If two such certificates must both be trusted by such a server,
delete `cert.pem` and `key.pem` from one `pki-dir`. The next start makes a
new pair, which the server has to trust once.

**The server's certificate is pinned on first use.** The certificate a
server offers arrives unauthenticated, so the first secured session records
its SHA-256 thumbprint in `pki-dir/trusted-servers/<host>_<port>.sha256`,
and every later connection refuses a different certificate, naming both
thumbprints and the file. If the server's certificate was replaced on
purpose, delete that file and the next connection trusts the new one. A
pinned server that later offers only unsecured endpoints is refused too, so
an endpoint list stripped down to None cannot take its place. The first
connection is trusted as found, so make it on a network you trust.

**A password is never sent on an unsecured channel.** With `username:` set,
an endpoint whose policy or mode is `None` is refused, so an endpoint list
stripped down to `None` cannot talk the "strongest offered" default into
sending credentials in the clear. Pin `security-mode: Sign` or
`SignAndEncrypt` whenever you set `username:`.

The driver dials the `endpoint:` you wrote even when the server advertises
a different address, so a server behind NAT or a VPN is reachable at the
address you can reach.

## Servers it has been run against

Every server below runs through the same scenarios (`opcua/matrix_test.go`).
open62541, Milo and the virtual IO-Link masters of `naut devices`
(nautilus's own server at the node ids the real masters use, through the
read scenarios where a master has nothing writable) run in every CI build.
Ignition and CODESYS run in a lab of ours, with the same test; their
results are in [Lab results](#lab-results). The scenarios are: import of a folder of one variable per scalar type (twice,
byte-identical), typed reads of every type the server is tested with
(the matrix's 13 scalar types; the IO-Link masters only the types their
process data has, and Ignition seven, as [Lab results](#lab-results)
says), a value that
changes on its own, an unknown node read Bad while the rest stay Good, a write of every
type read back, a write to a read-only node refused without dropping the
session, a binding whose namespace index is wrong read by its URI, and two
outages twice each: a restart (the server closes the connection) and a
blackhole (the server is frozen, so the link goes silent as it does when
a device loses power). Through both outages `__Online` falls, the
values hold as Stale, and the session comes back on its own with no
growth in goroutines.

| Server | Session | What to know |
|---|---|---|
| open62541 v1.4.22 (a fixture server built against the release) | Basic256Sha256 / SignAndEncrypt | Namespace 1 is the server's own application URI; a server's variables start at namespace 2. A write to a read-only node is refused with `Bad_NotWritable`. |
| Eclipse Milo 1.1.1 (its demo server 1.0.3) | Basic256Sha256 / SignAndEncrypt, once trusted | Refuses an untrusted client certificate by closing the connection and files it under `security/rejected`; moving it to `security/pki/trusted/certs` lets the next retry in, with no restart. Namespace 1's URI carries a per-install UUID, so a reinstalled server renumbers it. A write to a read-only node is refused with `Bad_UserAccessDenied`, not `Bad_NotWritable`. It advertises its own idea of its address (port 4840 inside the container); the driver dials the mapped port it was given. The demo address space includes a 25,000-node folder, so import with `--root`. |
| Virtual IO-Link masters: Pepperl+Fuchs ICE3, SICK SIG300, ifm AL1590 | None, anonymous (a master's default setup) | Node ids as the real masters serve them: strings with spaces (`ns=1;s=IOLM/Port 1/Attached Device/PDO Valid`), numeric ids four to a port (SICK, `ns=3;i=8012`), and the ifm's process data as hex text under the master's configured name. Only the P+F's PDO nodes take writes; the SICK and ifm run the read scenarios. A write to a read-only node is refused with `Bad_NotWritable`. |
| Ignition 8.3.10 (trial), its Generic Simulator device | Basic256Sha256 / SignAndEncrypt as a user from its `opcua-module` user source, once trusted | Out of the box the OPC UA server binds to `localhost` only, offers Basic256Sha256 / SignAndEncrypt alone and refuses anonymous sessions. Anonymous may browse and read once allowed; writes need a user. Refuses an untrusted client certificate by closing the connection and quarantines it; trusting it lets the next retry in, with no restart. Closes new connections from an address that opens them faster than about one a second (a bare EOF, often on GetEndpoints); the driver's retry ladder starts at 1 s and stays clear of it. Namespace 1's URI carries a per-install UUID. A write to a read-only node is refused with `Bad_UserAccessDenied`. A restart takes about 20 s to come back. Seven types: those the Generic Simulator has writable. |
| CODESYS Control Win V3 x64 3.5.22 (demo mode), a GVL of every scalar type | Basic256Sha256 / SignAndEncrypt as a runtime user, once trusted | See [Reading a CODESYS runtime](#reading-a-codesys-runtime). With user management on (the default), anonymous sessions are refused outright. The OPC UA server offers only None until it has a certificate of its own. Quarantines an untrusted client certificate (bare EOF); one moved to its trusted folder took effect after a runtime restart. Of several trusted client certificates sharing one subject it accepted only one, which is why each nautilus certificate now has its own subject. A `STRING` written over OPC UA takes ASCII only: anything else is refused with `Bad_OutOfRange` (use `WSTRING`). `LDT` is DateTime. A write to a read-only node is refused with `Bad_NotWritable`. |

Both read modes run every scenario. How quickly the driver notices: a
restart closes the connection, so `__Online` falls within a scan when
polling and within a second when subscribed; a blackhole is silent, so it
falls after one request timeout (5 s by default), plus up to a second when
subscribed. Once the server answers again the driver is back within one
retry step of the backoff ladder. Subscribed, every server above supported
`SetPublishingMode`, which the driver uses to check a subscribed session
(checked); the publishing interval each one revised the requested 200 ms
to is logged, not checked. The quiet check (a value that sits still for
five seconds, then changes once, must arrive as a notification with no
read) runs on every server with a writable Int32 or UInt32 node, so not
on the SICK and ifm masters, which have nothing writable.

### Lab results

Ignition and CODESYS run in a lab of ours rather than in CI, with the same
`opcua/matrix_test.go` scenarios. Run 2026-11-18 on nautilus commit
`73693ecf` (the first commit of PR #844; its later commits change only
this guide), each check in poll and subscribe mode
(quiet is subscribe only). The hooks these servers use are in
`opcua/matrix_test.go`, but the profiles that fill them (addresses, users,
certificates, the lab's start scripts) are a private lab configuration
not in this repository: `NAUTILUS_OPCUA_MATRIX=lab` on its own fails with
"names no server", so the table cannot be reproduced from the repository
alone.

| Server | Session | Types | import | reads | writes | remap | quiet | restart | blackhole |
|---|---|---|---|---|---|---|---|---|---|
| Ignition 8.3.10 (trial), Generic Simulator device | Basic256Sha256 / SignAndEncrypt, user | 7 of 13: Boolean, Int16, Int32, Int64, Float, Double, String (the simulator's writable ones) | pass | pass / pass | pass / pass | pass / pass | pass | pass / pass | pass / pass |
| CODESYS Control Win V3 x64 3.5.22.40 (demo mode) | Basic256Sha256 / SignAndEncrypt, runtime user | all 13 of the matrix | pass | pass / pass | pass / pass | pass / pass | pass | pass / pass | pass / pass |

## Reading an Ignition gateway

In *Connections > OPC > OPC UA Server Settings* (8.3), change three
things:

- **Bind Addresses**: set an address the controller can reach. The
  default is `localhost`.
- **Expose Tag Providers**: turn it on to read tags as well as devices.
- **User**: give the driver a user from the user source the server
  authenticates against (`opcua-module` by default) and set `username:`.
  Anonymous access is off by default; in the lab, once allowed, it could
  browse and read but writes needed a user.

Devices browse under namespace 1, for example
`ns=1;s=[Sim]_Meta:Writeable/WriteableInteger1`. Exposed tags browse
under `urn:inductiveautomation:ignition:opcua:tags`. The first secured
connect is refused until the client certificate is trusted under
*Connections > OPC > Security*, on the *Server* tab's quarantined list.
Run in the lab against 8.3 (see [Lab results](#lab-results)); 8.1 is not
run yet.

## Reading a CODESYS runtime

The runtime serves only the variables its application's **Symbol
Configuration** exports, with *Support OPC UA features* on. A GVL that
nothing references is left out of the build: give it
`{attribute 'linkalways'}`. Variables browse under
`DeviceSet/<device>/Resources/Application/GlobalVars`, with node ids such as
`ns=4;s=|var|CODESYS Control Win V3 x64.Application.GVL_Scalar.diInt32` in
namespace `CODESYSSPV3/3S/IecVarAccess`.

With the runtime's user management on (the default), anonymous OPC UA
sessions are refused. Sign in with a runtime user: `username:` with
`security-mode: SignAndEncrypt`. The OPC UA server offers only None until
it has a certificate. Make one in the *Security Screen*, or with
`cert-genselfsigned <n>` in the PLC shell, where `n` is `CmpOPCUAServer`'s
number in `cert-getapplist`. Then restart the runtime. The runtime files
the driver's certificate in `.pki/quarantine` on its first connect. Trust it in
the Security Screen, or move it to `.pki/trusted/cert` and restart the
runtime (a certificate moved there took effect only after a restart). Run in the lab against
CODESYS Control Win V3 x64 3.5.22.

