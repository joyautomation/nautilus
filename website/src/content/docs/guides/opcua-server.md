---
title: Serving OPC UA (preview)
description: Expose a controller's tags to any OPC UA client — browse, read and subscribe, with faithful types, quality as status codes and source timestamps — from one manifest line, and let a trusted SCADA client write the setpoints you open to it.
---

:::caution[Preview]
Still moving. The `opcua:` section and its `endpoint` are
meant to stay as they are, but session and subscription handling is being
reworked, and the limits named on this page (a never-delivered tag has no
node, a client must dial an advertised address) are tracked as
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
  # writable: ["*_SP", "Pump*.SpeedSP"]             # default: nothing is writable
  # anonymous-writes: true                          # lab only: no certificate needed to write
```

`naut run` (and a built binary) starts the server alongside the scan loop;
the section's presence turns it on (`opcua: {}` serves on the default).
`endpoint` is where it listens; `application-uri` is the name clients know
it by ([below](#what-a-client-sees)); `pki` is the certificate store the
secure endpoints use ([Security](#security)); `writable` names the tags a
trusted client may write ([Writes](#writes)); `anonymous-writes` lets any
client write them, for a lab ([Lab setups without
certificates](#lab-setups-without-certificates)). The endpoint defaults to the loopback interface —
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

The server is **read-only unless you open tags to writes**, and then only
to a client you trust. It offers an unsecured endpoint and three secure
ones, which open only to a client certificate you have approved; anyone
may read on the unsecured one, and only a trusted client on a secure one
may write the tags `opcua.writable` names. A refused write is answered
with a status code that says why, never silently dropped. See
[Security](#security) and [Writes](#writes). In a lab, where nobody wants
to manage certificates, one more line lets an anonymous client write too:
see [Lab setups without certificates](#lab-setups-without-certificates).

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
ns=1;s=Level                       a tag
ns=1;s=P101.Drive.Speed            a member of a UDT tag
ns=1;s=Pumps[2]                    an element of an array of UDTs
ns=1;s=Levels[2]                   an element of an array of scalars
ns=1;s=Conn.Inputs[17]             an element of an array member of a UDT
```

An element of an array of scalars is a Variable of its own, by node id,
though Browse does not list it: the array is one Variable holding an OPC UA
array, so a browsing client sees `Levels` and no children. The element node
reads as the element's type (a `BYTE` element is a `Byte`), with
`ValueRank` -1, and an index outside the declared bounds is
`Bad_NodeIdUnknown`. Its `AccessLevel` follows `opcua.writable` like any
scalar's, and a writer may write it, one element at a time (see
[What may be written](#what-may-be-written)). A client that addresses
bytes this way, as MHJ PLC-Lab is expected to, needs nothing more.

Tag names are IEC identifiers, so `ns=1;s=level` resolves to `Level`, and
the same goes for members and elements: `ns=1;s=conn.inputs[17]` is
`Conn.Inputs[17]`. A
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
the standard `Server` object's included. The server answers one request at
a time, so the services that can be asked for a great deal of work are
bounded, each limit advertised in the `Server` object's `OperationLimits`
and refused past it with `Bad_TooManyOperations`: a Browse (or BrowseNext)
names at most 500 nodes, a `TranslateBrowsePathsToNodeIds` at most 100
paths, and a `Call` at most 100 methods.

`TranslateBrowsePathsToNodeIds` resolves a path of browse names over the
same references a Browse lists, so `Objects/1:<project>/1:P101/1:Drive/1:Speed`
is `ns=1;s=P101.Drive.Speed` (asyncua's `get_child` is this service). A
name is compared exactly, case included, as OPC UA compares qualified
names: `1:level` does not find the tag Browse lists as `Level`, though a
node id of either spelling reads it. A path that reaches nothing is
`Bad_NoMatch`, and one of more than 32 elements `Bad_QueryTooComplex`.

`Call` answers each method it is asked for. The address space has no
methods of its own; of the four the `Server` object carries,
`GetMonitoredItems` (a subscription's server and client handles) and
`ResendData` (every reporting item of a subscription sent its current value
in the next cycle) run, for the calling session's own subscriptions
(another session's is `Bad_UserAccessDenied`).
`SetSubscriptionDurable` and `RequestServerStateChange` are
`Bad_NotExecutable`, and their `Executable` attribute reads false.

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
no audit events. `ServerDiagnostics.EnabledFlag` is false too, because the
server collects no diagnostics, and it is read-only. So `ServerDiagnostics`
holds only that flag: the standard lets a server with diagnostics off leave
out everything else under it (the server and sessions summaries, the
summary's counters, and the sampling-interval, subscription and session
arrays), and reading one answers `Bad_NodeIdUnknown`. `ServerCapabilities` has no
`MaxArrayLength` or `MaxStringLength`: the server sets no such limit of its
own (the message size bounds both), and the standard asks a server in that
case to leave them out.

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
| an enumeration | the enumeration's own DataType; the value is an `Int32`, the member's value (see [Enumerations](#enumerations)) |
| `REAL`, `LREAL` | `Double` |
| `TIME` | `Duration` (a `Double` of milliseconds) |
| `STRING` | `String` |
| `ARRAY [..] OF` a scalar | an array of the element's type |

An integer that does not fit its declared width — which a declared variable
never produces — is sent as `Int64` rather than wrapped. A `STRING` holding
`''` is sent as an empty `String`, never a null one, alone or in an array, so
a client that tells the two apart reads back the `''` the project holds.

### Enumerations

Each enumeration the project declares is served as an OPC UA enumeration
DataType, so a client shows the member names that the HTTP API and the
dashboard show. Take `TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE`:

- `Mode` is a DataType node, `ns=1;s=type:Mode`, a subtype of the standard
  `Enumeration`. It sits where the standard puts it, under
  `Types / DataTypes / BaseDataType / Enumeration`.
- Its `DataTypeDefinition` lists every member and its value:
  `Idle` 0, `Run` 10, `Fault` 11.
- It has one property naming the members. When the values run 0, 1, …, n-1,
  that is `EnumStrings`, an array of names indexed by value. Otherwise it is
  `EnumValues`, a list of value and name pairs, as `Mode`'s is.
- A tag, a UDT member or an array declared as `Mode` has `ns=1;s=type:Mode`
  as its `DataType`. Its value is still an `Int32`, the member's value, so
  `Mode1` reads `10`, and a client that knows the type shows `Run`.

asyncua (2.1) builds a Python enum for each from these, with
`await client.load_data_type_definitions()` or the older
`await client.load_enums()`, so `ua.Mode(10)` is `Mode.Run`. Any client
that reads an enumeration's `DataTypeDefinition`, `EnumStrings` or
`EnumValues` can show the names the same way. The type nodes are
read-only, and the set is the project's enumerations at the time the
server starts. Two tasks that declare one enumeration name with different
members are refused when the project loads.

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
`ModifyMonitoredItems` changes an item's sampling interval and client
handle, revised as `CreateMonitoredItems` revises them (the queue size is
always 1, and a filter is not acted on, there or here); the item is sampled
at the new interval from then on, and an item given a new client handle
hears its current value under it. `SetPublishingMode` pauses a
subscription: while publishing is off it sends keep-alives only, and keeps
the latest value of each item for when it is turned back on. A
subscription created with `PublishingEnabled` false starts paused.
`SetMonitoringMode` means what it answers: a `Disabled` or `Sampling`
item is silent until it is `Reporting` again, and then hears the current
value first; an item created `Disabled` is silent from the start.
`CloseSession` with `deleteSubscriptions` set (what client libraries send
by default) deletes the session's subscriptions and their items before it
answers; without it they run on until their lifetime runs out. A
subscription id is never issued twice.

`TransferSubscriptions` moves a subscription to another session of the
same client — typically a client that closed its session with
`deleteSubscriptions` false, or lost it, and opened a new one. Every
session here is anonymous, and OPC UA's rule for an anonymous user is that
only the same client application may take a subscription over, which
only a signed channel can show: both sessions must be on a
`SignAndEncrypt` endpoint (a [trusted
certificate](#security)) with the same `ApplicationUri`. A session on
`None`, or another application, is refused with `Bad_UserAccessDenied`;
asking for a subscription the session already has is `Bad_NothingToDo`.
The new session's publishes carry the subscription from then on — with
each reporting item's current value first, when it asks for
`sendInitialValues` — and the old session, if it is still open, is told
`Good_SubscriptionTransferred` (in answer to a Publish it has waiting, or
else its next one) and can no longer use it. A session that takes a
subscription back is not told it has lost it. No message is kept
for retransmission, so the transfer returns no available sequence numbers.
A session ends with its connection here (see [Sessions](#sessions)), so
what can be transferred is a subscription whose session is still open or
was closed with `deleteSubscriptions` false; a connection that drops takes
its session's subscriptions with it.

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

The secure endpoints keep what a client reads and writes private and
tamper-proof on the wire, and they are the only way to write:

| Session | Read, browse, subscribe | Write a tag `opcua.writable` opens |
|---|---|---|
| anonymous on `None` | yes | no: `Bad_UserAccessDenied` |
| anonymous on `None`, with `opcua.anonymous-writes: true` | yes | **yes** ([lab only](#lab-setups-without-certificates)) |
| on a `SignAndEncrypt` endpoint, from a trusted certificate | yes | **yes** |

A client certificate sent on the `None` endpoint makes no difference: the
channel is not secured, so the session is not a writer (or, with
`anonymous-writes`, is one, certificate or not). The standard
`Server` object and its variables are read-only for every session, so no
client can rewrite the `NamespaceArray` or `ServerStatus` other clients
rely on.

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
does the same. To withdraw trust, delete its file there. The client can
write nothing from its next Write on, even on the channel it already has:
for every Write request the server reads `trusted/` again and checks that
the certificate is still within its validity period, and if not answers
every item `Bad_UserAccessDenied`. It is refused on its next connection, and an
open channel is closed at its next key renewal. A channel's keys last an
hour at most, and a channel that does not renew in time is closed, so a
withdrawn client can go on reading for about an hour and a quarter. A certificate outside its validity period is refused with
`Bad_CertificateTimeInvalid`, trusted or not. There is no "accept
every certificate" switch; a lab that wants no certificates at all uses
[`anonymous-writes`](#lab-setups-without-certificates) on the `None`
endpoint instead. `--pki DIR` points both commands at another store; by
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

## Writes

A trusted client may write the tags you open to it. Nothing is writable
until you list it under `opcua.writable`:

```yaml
opcua:
  endpoint: opc.tcp://0.0.0.0:4840
  writable: ["*_SP", "Pump*.SpeedSP", "Recipe"]
```

Each entry is a glob over a tag name or a UDT member's path, matched in
the tag's declared casing. A pattern that matches a tag opens the tag and
everything below it, through members and array elements alike: `Recipe`
opens `ns=1;s=Recipe.Steps`, and `Drives`, an array of UDTs, opens
`ns=1;s=Drives[0].Speed`. One that matches a member opens that member and
everything below it: `Pump*.SpeedSP` opens `ns=1;s=Pump1.SpeedSP`, not
`Pump1.Running`. In a pattern `*` matches any run of characters and `?`
any one; `[n]` is the array index `n`, literally, and `[*]` any one index:
`Counts[2]` opens the element `Counts[2]`, `Counts[*]` every element of
`Counts`, and `Drives[*].Speed` every drive's `Speed` and nothing else of
it. `*` crosses `.` and `[`, so `Pump*.SpeedSP` also opens
`Pump[3].SpeedSP` and `Pump1.Drive.SpeedSP`. There are no character
classes: a `[` that is not `[n]` or `[*]` is refused (up to v0.19 patterns
were `path.Match` globs, where `Pump[12]` opened `Pump1` and `Pump2`). (`\` escapes the next character, so `'Drives\[0\]'`, in single
quotes, still names `Drives[0]`.) These are the same patterns, with the
same rule, as
[`sparkplug.writable`](/guides/sparkplug/), but the list is separate:
opening a setpoint to your SCADA over OPC UA does not open it to everyone
who can publish to the MQTT broker. If you want one list for both, use a
YAML anchor.

`naut check` refuses a malformed glob. It warns about:

- a pattern that matches nothing, which is usually a typo or a renamed tag;
- a pattern that matches only inputs;
- a pattern that opens no scalar but arrays of scalars: their elements are
  writable one at a time by node id (`ns=1;s=Trend[0]`), but a client
  that writes the whole array, or an `IndexRange`, is refused until array
  writes land;
- a pattern that opens only Objects with no scalar member, so nothing;
- an output it opens that a program writes.

### What may be written

- **The `Value` of a scalar tag or a struct member.** Any other attribute,
  an Object (a UDT tag, an array of UDTs, the project folder) and the
  standard `Server` object and its variables are `Bad_NotWritable`, for
  every session.
- **Not an input, even when a pattern matches it.** The driver replaces an
  input before every scan, so a client's value would be lost unread
  (`Bad_NotWritable`). Open the setpoint the logic reads instead.
- **An output when it is matched.** That is how a project that commands
  another controller passes a value through. A program that writes the same
  output overwrites the client's value at its next commit.
- **An array one element at a time.** An element of an array of scalars
  is written by its own node id, `ns=1;s=Levels[2]`, when a pattern opens
  the array (`Levels`), every element (`Levels[*]`) or the element
  (`Levels[2]`); the rest of the array is untouched. A whole
  array, or any write with an `IndexRange`, is `Bad_WriteNotSupported`
  for now.

`AccessLevel` tells a client what it may write: `CurrentRead` and
`CurrentWrite` on a scalar `opcua.writable` opens (not an input; an
element of an array is a scalar, the array itself is not), `CurrentRead`
elsewhere. `UserAccessLevel` is answered for the
session asking: the same as `AccessLevel` for a writer (an anonymous
session included, when `anonymous-writes` is on), `CurrentRead` for
anyone else, including a client whose certificate has been withdrawn or
has expired. `AccessLevelEx` follows `AccessLevel`.

### Conversions

A write lands exactly as sent, or is refused. Nothing is wrapped,
truncated or retyped.

| Node `DataType` | Accepted | Refused |
|---|---|---|
| `Boolean` | `Boolean` | anything else: `Bad_TypeMismatch` |
| `SByte` … `Int64`, `Byte` … `UInt64` | any integer type, checked against the declared width | `Float`, `Double`: `Bad_TypeMismatch`; outside the declared width: `Bad_OutOfRange` |
| `Double` (`REAL`, `LREAL`) | `Float`, `Double`; an integer within ±2^53 | NaN or infinity, an integer past ±2^53: `Bad_OutOfRange` |
| an enumeration (an `Int32`) | an integer that is a member's value | any other integer: `Bad_OutOfRange` |
| `Duration` (`TIME`) | a `Double` or an integer, in whole milliseconds | a fraction of a millisecond: `Bad_OutOfRange` |
| `String` | `String`; a null `String` is written as `''` | anything else: `Bad_TypeMismatch` |

The server accepts any integer type for an integer node, not only the
node's own `DataType`. Clients such as asyncua send a plain Python `5` as
`Int64`, and the declared width is what decides whether the value fits.
A `ULINT` or `LWORD` takes any 64-bit pattern, so a value you read from
one can be written back.

**Status code and timestamps.** A `DataValue` with no status code, or one
whose severity is Good (`Good`, `GoodClamped` and the other Good codes),
is a plain write. An Uncertain or Bad status code is
`Bad_WriteNotSupported`, because a tag's quality is its source's, not a
client's. Source and server timestamps are accepted and **ignored**. The
server dates the change itself, when the sweep sees it, as it does for
every other write. OPC UA allows a server to refuse timestamps with
`Bad_WriteNotSupported`, but most clients send one by default (asyncua's
`write_value` always does), so refusing them would refuse ordinary writes.

### Status codes

Each item of a Write gets its own status:

| Case | Status |
|---|---|
| the node id is nothing | `Bad_NodeIdUnknown` |
| the session is not a writer (the Value of any project Variable) | `Bad_UserAccessDenied` |
| not the `Value` attribute; an Object; a tag `opcua.writable` does not open; an input; any node of the standard `Server` object | `Bad_NotWritable` |
| a conversion the table above refuses | `Bad_TypeMismatch` or `Bad_OutOfRange` |
| an array or an `IndexRange`; an Uncertain or Bad status code | `Bad_WriteNotSupported` |
| the address is [forced](/guides/forcing/) | `Bad_Locked` |
| this replica is a standby | `Bad_InvalidState` |
| written | `Good` |

A Write may name up to 10 000 nodes: the `MaxNodesPerWrite` the `Server`
object advertises is the limit the server enforces, and a Write naming more
is refused whole with `Bad_TooManyOperations`.

`Good` means the value is in the tag store, not that the logic has acted
on it, just like a `204` from `POST /api/tags`.

### When a write takes effect

The value is in the store when the response goes out. The scan in progress
runs on its own snapshot and does not see it; the next scan does. If a
program writes the same tag, its commit wins. The items of one Write request
land together, under one lock, so a scan sees all of them or none: a high
and a low limit written in one request never straddle a scan. A refused
item does not hold back the others.

A subscriber hears the new value as it hears any change, within one sweep
plus its publishing interval, whichever session it belongs to. A written
setpoint is [retained](/guides/redundancy/#retained-state) like one
written over HTTP, when the manifest has a `retain:` section.

A [forced](/guides/forcing/) address holds its forced value against every
writer, so a write to it, to a member of it, or to the tag that holds it
is refused with `Bad_Locked`. Change or remove the force first.

With [redundancy](/guides/redundancy/#redundancy), only the leader takes
writes. A standby's store is stale and does not scan, so it answers every
write `Bad_InvalidState`. The replica is asked once per request, so a
request lands whole or not at all if leadership moves while it is
answered, and its `opcua-server` row says it is a standby.
Unlike the HTTP API, a standby cannot pass an OPC UA write on to the
leader. A client that is answered `Bad_InvalidState` should reconnect, and
a Service that spreads connections over the replicas can land it on a
standby again.

### Audit

Every landed item is logged on the console as an `opcua: write` line naming
the node, the old and the new value, the session, the client's
`ApplicationUri`, the first 16 digits of its certificate thumbprint and its
address; an anonymous writer's line has no URI or thumbprint and says
`anonymous=true`. A refused item is an `opcua: write refused` warning with the
status, the reason and the same client fields, `anonymous=true` included
for an anonymous writer. A writer's refusal on a node it may know (a
trusted client's, or an anonymous one's with
[`anonymous-writes`](#lab-setups-without-certificates) on) is logged once
per client, node and reason. Every other refusal (a client that is no
writer, such as an anonymous one with the switch off, or a node id that is
nothing) is one line per client and reason, saying how many items of the
first such request were refused for that reason: those node ids are
whatever the client chose to send.
Past 64 lines in an hour a client's refusals are only counted, and one
line says so; the next hour it is heard from again. A client retrying every second, or
sending thousands of junk node ids, does not flood the log. The `opcua-server`
row counts both (`writes`, `writes refused`).

### Writing from asyncua

With the client's certificate trusted ([Approving a
client](#approving-a-client)), asyncua's ordinary `write_value` works:

```python
from asyncua import Client, ua

client = Client("opc.tcp://controller:4840")
client.application_uri = "urn:acme:scada"   # the URI in client-cert.der
await client.set_security_string(
    "Basic256Sha256,SignAndEncrypt,client-cert.der,client-key.pem")
async with client:
    await client.get_node("ns=1;s=Level_SP").write_value(4.25)        # REAL
    await client.get_node("ns=1;s=Pump1.SpeedSP").write_value(1450)   # DINT: any integer type
    await client.get_node("ns=1;s=Mode_SP").write_value(
        ua.Variant(10, ua.VariantType.Int32))                          # an enumeration member's value
    await client.get_node("ns=1;s=Delay_SP").write_value(1500.0)      # TIME, in ms
```

A refused write raises `ua.UaStatusCodeError` carrying the status code.

## Lab setups without certificates

In a lab, a simulator often plays the plant: MHJ PLC-Lab or Factory I/O
writes the sensors the logic reads and reads the outputs it drives, and
connects anonymously on the `None` endpoint. Nothing on that network needs
protecting, and approving certificates is a chore nobody asked for. One
line lets an anonymous client write:

```yaml
name: conveyor-lab

tasks:
  - program: program.st
    scan: 50ms

tags:
  - { name: PartAtEnd, role: setpoint, type: BOOL, desc: Written by the simulator }
  - { name: StartPB,   role: setpoint, type: BOOL, desc: Written by the simulator }
  - { name: Conveyor,  role: output,   type: BOOL, desc: Read by the simulator }

opcua:
  endpoint: opc.tcp://0.0.0.0:4840   # 127.0.0.1 when the simulator runs on this machine
  writable: ["PartAtEnd", "StartPB"]
  anonymous-writes: true
```

The simulator here runs on another PC, so the server listens on every
interface. Prefer localhost (`opc.tcp://127.0.0.1:4840`) when the simulator
runs on the same machine: then only clients on that machine can reach it.

`naut check` passes this manifest with one warning, and that warning is
expected: it is there so the switch is never on unnoticed. On `0.0.0.0` it
reads:

```text
nautilus.yaml: warning: opcua.anonymous-writes is on and opc.tcp://0.0.0.0:4840 listens beyond loopback: ANY client that can reach it over the network can write the tags opcua.writable opens (PartAtEnd, StartPB) without a certificate, and only its address is logged — use it on a lab network only, never on a plant network; when the client runs on this machine, listen on 127.0.0.1 instead
```

On `127.0.0.1` (or the default `localhost`) it is milder: any client that
can reach the endpoint, on this machine only, can write those tags without
a certificate.

With `anonymous-writes: true`, every session on the `None` endpoint is a
writer, with no certificate. Everything else stays as it is:

- **`writable` still decides what may be written.** With `anonymous-writes`
  on and no `writable` list, nothing is writable; `naut check` warns about
  that instead. A tag no pattern opens is still `Bad_NotWritable`.
- **The same rules as a trusted writer.** Conversions, status codes,
  forces (`Bad_Locked`), the standby gate (`Bad_InvalidState`), the
  10 000-node limit and arrays (`Bad_WriteNotSupported`) all answer the
  same. `UserAccessLevel` reads `CurrentRead` and `CurrentWrite` for an
  anonymous session on a tag it may write, so a client offers the write.
- **Declare what the simulator writes as `setpoint` tags, not inputs.** An
  input belongs to a driver, which replaces it before every scan, so it is
  never writable over OPC UA. What the simulator only reads, such as
  `Conveyor`, needs no entry under `writable`.
- **The secure endpoints still work** for a trusted client, when the
  certificate store is there, and only by its certificate: one taken out of
  the trust list writes nothing more on its secure channel.

The server says it is open, every time it starts: a warning when it
listens on loopback (only clients on that machine can write), and an
error when it listens beyond it, since then **anyone who can reach the
port may write**, and the audit and refusal lines can only name their
address (`anonymous=true`, no certificate). `naut run`'s banner adds
`(anonymous writes on)`, and the dashboard's OPC UA card ends its sentence
with `writable by anonymous clients` ([Status](#status)). Use it on a lab
network only, never on a plant network. Off is the default.

### MHJ PLC-Lab

PLC-Lab drives a CODESYS controller through a container named
`PlcLabConnector`: input bytes it writes, output bytes it reads, and two
area tables saying which bytes it uses. A nautilus project can be that
controller with no bridge in between: declare the container as one struct
tag of that shape (a `state` tag, since PLC-Lab writes into it), open what
PLC-Lab writes under `writable`, and copy bytes to and from your logic in
a program. [`examples/plclab`](https://github.com/joyautomation/nautilus/tree/main/examples/plclab)
is such a project, with the connector type, the copy program and a small
tank in ladder; its README has PLC-Lab's settings (address mode
"Address mode for CodeSys systems", anonymous) and the I/O map.

```yaml
opcua:
  endpoint: opc.tcp://localhost:4840
  anonymous-writes: true
  writable:
    - PlcLabConnector.Inputs[*]     # every element, each by its own node id
    - PlcLabConnector.AreaInputs
    - PlcLabConnector.AreaOutputs
```

PLC-Lab is expected to reach the bytes as elements,
`ns=1;s=PlcLabConnector.Inputs[17]`, which nautilus serves (see [What a
client sees](#what-a-client-sees)): that is the CODESYS shape as MHJ's
manual describes it. The example is checked against a client that behaves
that way; it has not yet been run against PLC-Lab itself, and these are
still open:

- **The namespace and node id form.** nautilus serves the connector in
  namespace 1 (`urn:joyautomation:nautilus:<project>`) with the address as
  the string id. A PLC-Lab that expects a fixed namespace index or URI, or
  a CODESYS-style id such as `ns=4;s=|var|<device>.Application.PlcLabConnector.Inputs[17]`,
  would not find it.
- **Element writes, IndexRange or whole arrays.** Single elements by node id
  work. A write of `Inputs` with an `IndexRange`, or of the whole array, is
  `Bad_WriteNotSupported` until array writes land.
- **Browsing down to elements.** Browse lists `Inputs` as one array
  Variable with no element nodes under it; a client that browses to each
  byte rather than building its node id would not find them.
- **What PLC-Lab writes at connect.** If it also writes `Outputs` or the
  `SIZE_` words, those writes are refused (`Bad_NotWritable`), as nothing
  opens them.
- **Word and double-word operands.** In CODESYS addressing `IWn` is image
  bytes `2n..2n+1` (and `IDn` bytes `4n..4n+3`); the example maps single
  bits of byte 0 only, and does not map words.

If you run it against PLC-Lab, please report what happened, with `naut
run`'s console output (every refused write is logged with its node id and
reason).

## Status

The dashboard's driver cards and `GET /api/drivers` carry an `opcua-server`
row. The dashboard's OPC UA card shows:

- its state (connected while it listens) and the endpoint;
- a sentence: `Serving 12 tags · 1 subscription, 8 monitored items` while
  it listens, followed by ` · writable by anonymous clients` when
  [`anonymous-writes`](#lab-setups-without-certificates) is on and
  `writable` opens something, then by the certificate warning from 30 days
  before the server certificate expires, then how long it has been up;
  `Not listening — ` and the error (repeated on a line of its own) when it
  could not listen; `Not started` before it starts;
- the counts `tags`, `subscriptions`, `monitored items`, `writes` (landed)
  and `writes refused`.

`GET /api/drivers` carries the same, and in the row's `extra`: `endpoint`,
`access` (`read-only`, `writable by trusted clients` or `writable by
anonymous clients`), `writes`, `writesRefused`, `writable` (the patterns,
when there are any), `anonymousWrites: true` when the switch is on (even
with nothing writable), `standby: true` on a standby replica, and
`certThumbprint`, `certExpiresMs` and `certWarning` for the server
certificate. A port already in use is reported there and on stderr at start
(`continuing without it`); the controller runs regardless.

Stopping the controller (Ctrl+C, or SIGTERM from a service manager) closes
every client's connection at once, sessions and subscriptions included, and
gives the HTTP port back first: `naut run` started again straight away gets
its dashboard, tag API and OPC UA endpoint.
