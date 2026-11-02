---
title: Serving OPC UA (preview)
description: Expose a controller's tags to any OPC UA client — browse, read and subscribe, with faithful types, quality as status codes and source timestamps — from one manifest line.
---

:::caution[Preview]
Read-only, and still moving. The `opcua:` section and its `endpoint` are
meant to stay as they are, but session and subscription handling is being
reworked, and the limits named on this page (a never-delivered tag has no
node, several subscription services are unsupported, a client must dial an
advertised address) are tracked as
issues and may change between releases. Try it against your client before
you depend on it.
:::

An `opcua:` section in `nautilus.yaml` serves the controller's tag store as
an **OPC UA address space**. Any OPC UA client — a SCADA gateway, a
historian, UAExpert — connects to the endpoint, browses the project folder
under `Objects`, and reads or subscribes to the tags. Names, types, current
values and each tag's `desc:` and `unit:` arrive as data, not as
configuration you re-enter on the client side.

```yaml
opcua:
  endpoint: opc.tcp://0.0.0.0:4840   # default opc.tcp://localhost:4840
  # application-uri: urn:acme:plant7:lift-station   # default urn:joyautomation:nautilus:<project>
  # pki: /var/lib/nautilus/pki                     # default ./pki
```

`naut run` (and a built binary) starts the server alongside the scan loop;
the section's presence turns it on (`opcua: {}` serves on the default).
`endpoint` is where it listens; `application-uri` is the name clients know
it by ([below](#what-a-client-sees)); `pki` is the certificate store the
secure endpoints use ([Security](#security)). The endpoint defaults to the loopback interface —
`localhost` listens on `127.0.0.1` — so putting a controller on the network
is a line you write: `opc.tcp://0.0.0.0:4840` listens on every interface.
`naut check` refuses an endpoint that is not `opc.tcp://host:port` (a bare
`host:port` is read as `opc.tcp://host:port`).

An OPC UA client finds an endpoint by its exact URL, so the server
advertises one per name it answers to: the host you wrote, every interface
address when you wrote `0.0.0.0`, the machine's host name, and loopback. A
client may dial any of them. One that reaches the server through a URL it
never heard of (a NAT address, a DNS alias) is given the whole list, by
GetEndpoints and again in the CreateSession answer, so it can open its
session through the address it used.

The server is **read-only**: a write is answered `Bad_UserAccessDenied`,
never silently dropped. It offers an unsecured endpoint and three secure
ones, which open only to a client certificate you have approved; see
[Security](#security).

## What a client sees

Under `Objects` there is one folder named after the project. Inside it,
one node per tag:

- A scalar tag (`BOOL`, `INT`, `REAL`, `STRING`, `TIME`, an enumeration) is
  a **Variable**.
- A UDT tag is an **Object** whose members are child nodes — nested UDTs
  nest. (A function-block instance is program state, not a tag, so it has
  no node.)
- An array of scalars is a **Variable** holding an OPC UA array
  (`ValueRank` 1, `ArrayDimensions` the declared length).
- An array of UDTs is an **Object** with one child per element, indexed from
  the declared lower bound: `Pumps[1]`, `Pumps[2]`.
- A two-dimensional array (`ARRAY[0..1, 0..2] OF INT`) is an **Object** of
  one-dimensional rows, `Grid[0]`, `Grid[1]`, each a Variable holding an
  array.

Node ids are string ids in the tag store's own address form, in the
project's namespace — index 1, the entry after the standard
`http://opcfoundation.org/UA/` in the server's `NamespaceArray`, with the
URI `urn:joyautomation:nautilus:<project>` — so a client that already knows
a nautilus address can write the node id down without browsing for it:

```
ns=1;s=Level               a tag
ns=1;s=P101.Drive.Speed    a member of a UDT tag
ns=1;s=Levels[2]           an element of an array of UDTs
```

Tag names are IEC identifiers, so `ns=1;s=level` resolves to `Level`. A
tag's `desc:` and `unit:` from the [tag model](/guides/tag-model/) become
the node's `Description` — `Wet well level [ft]`.

A Read of an array honours its `IndexRange`: `2` is one element, `1:3` a
slice, counted from 0. A `STRING` is an array of characters, so `0:3` on
one is its first four, and an array of strings takes a second dimension
(`1,0` is the first character of the second string). A range that starts
past the end of the value, or a range on a number or a boolean, is
`Bad_IndexRangeNoData`; one that starts inside it and runs past the end is
what there is, so `0:9` on four elements is all four, and in an array of
strings a string too short for the range is `""`. A monitored item's `IndexRange` means the same:
the item hears its slice, and only when the slice changes. One Read may
name up to 10 000 nodes —
the `MaxNodesPerRead` the `Server` object advertises is the limit the
server enforces, with `Bad_TooManyOperations` past it — so a project of a
few thousand tags is still one call. A Browse lists each reference once,
the standard `Server` object's included.

The server says it is nautilus. Its `ApplicationUri`, in every endpoint's
description and as the only entry of the `Server` object's `ServerArray`,
is `urn:joyautomation:nautilus:<project>` (the name percent-encoded, so
`Lift Station` is `Lift%20Station`). It names the controller, not the
machine: it is the same after a restart, on a new host, and on every
replica of a [cluster deployment](/guides/deployment/), so a historian or
SCADA gateway that keys its connection on it finds the same server
whichever replica answers. Two separate controllers running a project of
the same name on one network should each set `opcua.application-uri` to a
URI of their own; `naut check` refuses one that is not an absolute URI.
The `Server` object's variables are the standard ones, with their
standard names, data types and places under `ServerStatus` and
`BuildInfo`. `ServerStatus.BuildInfo`
names the product (`ProductUri` `urn:joyautomation:nautilus`, Joy
Automation, Nautilus), the version `naut version` prints, the commit it
was built from as the build number, and that commit's date. `ServiceLevel`
is 255 while the server runs, and `Auditing` is false: the server writes
no audit events.

## Types

The tag store holds every integer as 64 bits and every real as a double,
but a `DINT` is still a `DINT`: each node's `DataType` is the type the tag
was declared as.

| IEC type | OPC UA DataType |
| --- | --- |
| `BOOL` | `Boolean` |
| `SINT` / `USINT`, `BYTE` | `SByte` / `Byte` |
| `INT` / `UINT`, `WORD` | `Int16` / `UInt16` |
| `DINT` / `UDINT`, `DWORD` | `Int32` / `UInt32` |
| `LINT` / `ULINT`, `LWORD` | `Int64` / `UInt64` |
| an enumeration | `Int32` (the member's value) |
| `REAL`, `LREAL` | `Double` |
| `TIME` | `Duration` (a `Double` of milliseconds) |
| `STRING` | `String` |
| `ARRAY [..] OF` a scalar | an array of the element's type |

An integer that does not fit its declared width — which a declared variable
never produces — is sent as `Int64` rather than wrapped. A `STRING` holding
`''` is sent as an empty `String`, never a null one, alone or in an array, so
a client that tells the two apart reads back the `''` the project holds.

## Quality and timestamps

Every value carries the tag's [quality](/guides/tag-model/#what-a-value-is-worth-quality) as its
OPC UA status code, so a client that only checks the severity bit treats a
dead reading the way an HMI treats a bad binding:

| Quality | Status |
| --- | --- |
| good | `Good` |
| stale — the source stopped refreshing it | `Uncertain_LastUsableValue` |
| bad — the source reports it untrustworthy | `Bad_DeviceFailure` |
| not connected — never delivered | `Bad_NotConnected` |

A member of a UDT is exactly as trustworthy as its tag. `SourceTimestamp`
is the time the tag's current value was first seen — the server's start,
for a value nothing has changed since — and moves only when the value
does: a member of a UDT keeps its own timestamp while a sibling member
changes. `ServerTimestamp` is the time of the read. A change of quality
alone, with the value unchanged, is a data change: a subscriber hears the
value go `Uncertain_LastUsableValue` when its source stops answering, and
`Good` again when it comes back.

An input that has never been delivered still has a node. A protocol
driver's input (Modbus, EtherNet/IP, Sparkplug host, the IT pollers) is
seeded with its `init:` or zero (see [Inputs from a device that is
down](/guides/tag-model/#inputs-from-a-device-that-is-down)), so it reads
that seed with `Bad_NotConnected` until the device answers. A local-I/O
input (`plcnext`, `optommp`) is not seeded, and a program that reads it
before its first value still faults the scan. Its node is there from the
start all the same: typed as declared, with no value, and `Bad_NotConnected`
(or the driver's own Bad status, when it reports one for the tag).
The exception is an input whose type nothing declares (no `type:`, and no
program declares it). It has no node until its first value arrives, so a
client that browsed before then must browse again.

One limit of the current server, found by its black-box runs and tracked as
an issue: a subscription's items are fixed once created, and it cannot be
paused or moved to a new session (`ModifyMonitoredItems`,
`SetPublishingMode` and `TransferSubscriptions`, like
`TranslateBrowsePathsToNodeIds` and `Call`, answer `Bad_ServiceUnsupported`;
[#431](https://github.com/joyautomation/nautilus/issues/431)).

## Subscriptions

A client may create subscriptions and monitor any Variable. The server
never polls a copy of the tag store: every 100 ms it asks the store which
tags changed since the last sweep — one integer compare per tag, using the
same write generations the HTTP stream uses — and notifies only the
monitored items whose value or status actually changed; a member of a UDT
is quiet while its siblings move. A subscriber hears of a change within
one sweep plus its own publishing interval; `MinimumSamplingInterval`
reports the sweep; a requested sampling interval is revised to the nearest
multiple of it, and the item is sampled at that rate. A requested publishing
interval is revised the same way — `0` asks for the fastest and is granted
the sweep; nothing slower than an hour is granted — rather than booked as
asked. `ModifySubscription` changes a live subscription's publishing
interval and its lifetime and keep-alive counts, revised the same way, and
answers once the subscription runs at them: a client can slow a busy
subscription down, or speed it up, without deleting and recreating it.
`SetMonitoringMode` means what it answers: a `Disabled` or `Sampling`
item is silent until it is `Reporting` again, and then hears the current
value first; an item created `Disabled` is silent from the start.
`CloseSession` with `deleteSubscriptions` set (what client libraries send
by default) deletes the session's subscriptions and their items before it
answers; without it they run on until their lifetime runs out. A
subscription id is never issued twice.

An item that cannot be monitored — a node id that is not a node, or a
Value on an Object or the folder — is refused when it is created, with the
code a Read of it gives (`Bad_NodeIdUnknown`, `Bad_AttributeIdInvalid`),
not created and left to fail inside a notification.

## Security

### Endpoints

Each address the server advertises carries four endpoints:

| Security policy | Mode | Who may open it |
|---|---|---|
| `None` | `None` | anyone, anonymously |
| `Basic256Sha256` | `SignAndEncrypt` | a client whose certificate is trusted |
| `Aes128_Sha256_RsaOaep` | `SignAndEncrypt` | the same |
| `Aes256_Sha256_RsaPss` | `SignAndEncrypt` | the same |

Nothing else is accepted. A client that asks for `Sign` without encryption,
or for the deprecated `Basic128Rsa15` or `Basic256`, is refused with
`Bad_SecurityPolicyRejected` when it opens its channel. A secure channel
renews its keys under the security it opened with; a renewal that asks for
other security is refused.

Every node is read-only on every endpoint (`AccessLevel` is `CurrentRead`),
including the standard `Server` object and its variables, so no session can
rewrite the `NamespaceArray` or `ServerStatus` other clients rely on. The
secure endpoints keep what a client reads private and tamper-proof on the
wire. They grant no extra rights yet.

### The certificate store

The secure endpoints need a certificate. On its first start the server
makes one in its certificate store, the directory `opcua.pki` names
(default `./pki`, relative to the directory `naut run` starts in):

```
pki/
  own/cert.der            the server's certificate
  own/private/key.pem     its private key (readable by the owner only)
  trusted/certs/          client certificates the server accepts
  rejected/certs/         client certificates it refused
```

The certificate it makes is the kind OPC UA asks for: RSA-2048 with
SHA-256, self-signed, valid five years. It names the server's
`application-uri` and every host name and address the server advertises.
A certificate already in `own/` is used as it is and never replaced, since
a new certificate means every client has to trust the server again; its
URI is the one the server announces. If the machine's name later changes,
the server logs which names the certificate lacks. Empty `own/` and
restart to make a new one.

Keep the store out of version control. `naut new` puts `/pki/` in
`.gitignore`, and `naut build` never embeds a certificate store, so a built
binary carries no key. In a cluster, mount one store (a Secret) into every
replica, so a client trusts one certificate whichever replica answers.

### Approving a client

A client's certificate must be in `trusted/certs/` before its secure
channel opens. Approving a new SCADA or historian takes one refused attempt
and one command:

1. Point the client at a secure endpoint. The server refuses it with
   `Bad_CertificateUntrusted` and files its certificate in
   `rejected/certs/`, named by its thumbprint.
2. On the controller, list the store:

   ```
   $ naut opcua certs
   Server certificate (pki/own/cert.der)
     thumbprint  90b78630b79b4acfa3e0c6a3132c050f7fcf8c4a
     uri         urn:joyautomation:nautilus:lift-station
     expires     2031-11-01
   …
   Rejected clients (pki/rejected/certs): 1
     THUMBPRINT                                APPLICATION URI   SUBJECT   EXPIRES
     5c1f0e7a…                                 urn:acme:scada    CN=SCADA  2027-03-01
   ```

   Compare the thumbprint with the one the client shows for its own
   certificate.
3. Approve it:

   ```
   $ naut opcua trust 5c1f0e7a9b3d2c41
   ```

   Give at least 16 hex digits, or paste the whole thumbprint. Anyone who
   can reach the port can file a certificate in `rejected/`, so a short
   prefix could match one made to imitate it. The server reads `trusted/`
   on every connection, so the client's next attempt connects, with no
   restart. `rejected/` keeps the 100 most recent refusals.

Copying a certificate into `trusted/certs/` (DER or PEM, any file name)
does the same. To withdraw trust, delete its file there: the client is
refused on its next connection, and an open channel at its next key
renewal. A channel's keys last an hour at most, and a channel that does not
renew in time is closed, so a withdrawn client is cut off within about an
hour and a quarter. A certificate outside its validity period is refused with
`Bad_CertificateTimeInvalid`, trusted or not. There is no "accept
everything" switch. `--pki DIR` points both commands at another store; by
default they use the `opcua.pki` of the `nautilus.yaml` in the working
directory, else `./pki`.

The client must also present the certificate its channel was opened with
when it creates its session, with the `ApplicationUri` that certificate
names. A client that announces another URI is refused with
`Bad_CertificateUriInvalid`. In asyncua:

```python
from asyncua import Client

client = Client("opc.tcp://controller:4840")
client.application_uri = "urn:acme:scada"   # the URI in client-cert.der
await client.set_security_string(
    "Basic256Sha256,SignAndEncrypt,client-cert.der,client-key.pem")
async with client:
    print(await client.nodes.server_state.read_value())
```

### Sessions

Sessions are anonymous on every endpoint; no user names are offered yet. A
session belongs to the channel that created it. Its authentication token is
a random 32-byte value, and a request that presents it on any other
connection is refused with `Bad_SecureChannelIdInvalid`. A client that loses
its connection opens a new session.

### Writes

Writes are planned for a later release. They will land only for tags the
manifest opens to OPC UA, and only on a secure channel from a trusted
client certificate. Until then, a controller that must take setpoints from
a SCADA host should take them over [Sparkplug](/guides/sparkplug/) or the
HTTP API.

## Status

The dashboard's driver cards and `GET /api/drivers` carry an `opcua-server`
row: the endpoint, whether it is listening, how many subscriptions and
monitored items clients hold, and the server certificate's thumbprint and
expiry, with a warning from 30 days before it expires. A port already in use is reported there and
on stderr at start (`continuing without it`); the controller runs
regardless.

Stopping the controller (Ctrl+C, or SIGTERM from a service manager) closes
every client's connection at once, sessions and subscriptions included, and
gives the HTTP port back first: `naut run` started again straight away gets
its dashboard, tag API and OPC UA endpoint.
