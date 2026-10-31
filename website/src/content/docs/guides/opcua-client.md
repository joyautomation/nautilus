---
title: Reading OPC UA (preview)
description: Poll an OPC UA server (an Ignition gateway, a PLC's embedded server, a Kepware) from a committed manifest, with per-tag quality, typed writes you opt into per tag, and namespace-safe node ids.
---

:::caution[Preview]
Tested against gopcua's in-process server only (including a
`Basic256Sha256 / SignAndEncrypt` session). It has not yet been run against
Ignition, Kepware, node-opcua or a PLC's embedded server. Try it against
your server before you depend on it.
:::

A `driver: {type: opcua}` section makes nautilus an OPC UA **client**: it
polls one server's variables into the tag store. This is the opposite
direction to the [`opcua:` section](/guides/opcua-server/), which *serves*
this project's own tags to OPC UA clients; a project can do both.
Everything the driver polls comes from a browse of the server that
`naut opcua import` turns into a committed manifest and a committed tag
file, the way `naut eip import` and `naut modbus import` do.

```yaml
driver:
  type: opcua
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

Variables whose type is not a scalar built-in (structures, enumerations)
and arrays are listed as skipped, with the reason. `--tags` keeps a subset
by glob.

The tag file also declares the driver's `<name>__Online` companion. Its
name comes from `--name` when given, else from the `opcua` driver in the
project's `nautilus.yaml` whose `endpoint:` matches (then whose `manifest:`
matches, then the only one), else `opcua__Online`. The import prints which.

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
  `name:` (`opcua__Online` for an unnamed one; `opcua-2` becomes
  `opcua_2__Online`).
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
drops what it queued.

## Security

`security:` names the policy, `security-mode:` the mode. Leave both out and
the driver takes the strongest endpoint the server offers. Any policy but
`None` needs a client certificate, generated self-signed into `pki-dir` on
first use, with the application URI as a URI SAN. The server must trust it
(on Ignition: *Config > OPC UA > Security > Trusted Certificates*). The
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

## Reading an Ignition gateway

Enable the OPC UA server (*Config > OPC UA > Server Settings*), bind it to
an address the controller can reach, and turn on **Expose Tag Providers**.
Every provider then browses under `Devices`. Not yet run against a gateway.
