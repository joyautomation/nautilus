---
title: Reading OPC UA (preview)
description: Poll an OPC UA server (an Ignition gateway, a PLC's embedded server, a Kepware) from a committed manifest, with per-tag quality, typed writes you opt into per tag, and namespace-safe node ids.
---

:::caution[Preview]
Tested against gopcua's in-process server, open62541 and Eclipse Milo
(see [Servers it has been run against](#servers-it-has-been-run-against)),
each over `Basic256Sha256 / SignAndEncrypt`. It has not yet been run against
Ignition, Kepware or a PLC's embedded server. Try it against your server
before you depend on it.
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
  endpoint: opc.tcp://10.0.0.5:4840
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
naut opcua import --endpoint opc.tcp://10.0.0.5:4840 \
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

## Polling, quality and `__Online`

Each scan class is one batched Read on its own timer, chunked to the
server's advertised `MaxNodesPerRead` (halved if the server answers
`Bad_TooManyOperations`). Subscriptions are not used.

- `<name>__Online` (BOOL) is true once the session is up and every scan
  class has been read on it, false otherwise. Its name follows the driver's
  `name:` (`opcua_client__Online` for an unnamed one; a second unnamed
  one, `opcua-client-2`, gets `opcua_client_2__Online`).
- A node the server refuses (unknown id, wrong namespace, a dead device
  behind a gateway, an array) is **Bad** while the rest of the class keeps
  delivering. `Uncertain` statuses count as not good, on purpose.
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
driver keeps: the first snapshot after start is a baseline, not a command;
an unchanged value is not a command; a command made while the session is
down goes out on reconnect (the last value per tag); a write the server
refuses on a healthy session is dropped and reported, not retried forever.
Under `redundancy:` only the leader writes, and leadership is checked
again when queued commands go out: a replica that lost it during an outage
drops what it queued. When the write gate reopens, every output goes out
once more with the program's value, changed or not, since another
replica may have written the nodes in between, and a replica that started
as a standby has no start-up baseline, so its first push as leader
commands. The driver sees the gate by looking at it, at least once a
second while connected and on every connect attempt: a flip between two
looks is not seen.

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

Every server below runs in CI (`opcua/matrix_test.go`) through the same
scenarios (the virtual IO-Link masters of `naut devices`, nautilus's own
server at the node ids the real masters use, through the read ones where a
master has nothing writable): import of a folder of one variable per scalar type (twice,
byte-identical), typed reads of every type, a value that changes on its
own, an unknown node read Bad while the rest stay Good, a write of every
type read back, a write to a read-only node refused without dropping the
session, a binding whose namespace index is wrong read by its URI, and two
outages twice each: a restart (the server closes the connection) and a
blackhole (the container is paused, so the link goes silent as it does
when a device loses power). Through both outages `__Online` falls, the
values hold as Stale, and the session comes back on its own with no
growth in goroutines.

| Server | Session | What to know |
|---|---|---|
| open62541 v1.4.22 (a fixture server built against the release) | Basic256Sha256 / SignAndEncrypt | Namespace 1 is the server's own application URI; a server's variables start at namespace 2. A write to a read-only node is refused with `Bad_NotWritable`. |
| Eclipse Milo 1.1.1 (its demo server 1.0.3) | Basic256Sha256 / SignAndEncrypt, once trusted | Refuses an untrusted client certificate by closing the connection and files it under `security/rejected`; moving it to `security/pki/trusted/certs` lets the next retry in, with no restart. Namespace 1's URI carries a per-install UUID, so a reinstalled server renumbers it. A write to a read-only node is refused with `Bad_UserAccessDenied`, not `Bad_NotWritable`. It advertises its own idea of its address (port 4840 inside the container); the driver dials the mapped port it was given. The demo address space includes a 25,000-node folder, so import with `--root`. |
| Virtual IO-Link masters: Pepperl+Fuchs ICE3, SICK SIG300, ifm AL1590 | None, anonymous (a master's default setup) | Node ids as the real masters serve them: strings with spaces (`ns=1;s=IOLM/Port 1/Attached Device/PDO Valid`), numeric ids four to a port (SICK, `ns=3;i=8012`), and the ifm's process data as hex text under the master's configured name. Only the P+F's PDO nodes take writes; the SICK and ifm run the read scenarios. A write to a read-only node is refused with `Bad_NotWritable`. |

How quickly the driver notices: a restart closes the connection, so the
next read fails at once and `__Online` falls within a scan; a blackhole is
silent, so it falls after one request timeout (5 s by default). Once the
server answers again the driver is back within one retry step of the
backoff ladder.

## Reading an Ignition gateway

Enable the OPC UA server (*Config > OPC UA > Server Settings*), bind it to
an address the controller can reach, and turn on **Expose Tag Providers**.
Every provider then browses under `Devices`. Not yet run against a gateway.
