---
title: Serving OPC UA
description: Expose a controller's tags to any OPC UA client — browse, read and subscribe, with faithful types, quality as status codes and source timestamps — from one manifest line.
---

An `opcua:` section in `nautilus.yaml` serves the controller's tag store as
an **OPC UA address space**. Any OPC UA client — a SCADA gateway, a
historian, UAExpert — connects to the endpoint, browses the project folder
under `Objects`, and reads or subscribes to the tags. Names, types, current
values and each tag's `desc:` and `unit:` arrive as data, not as
configuration you re-enter on the client side.

```yaml
opcua:
  endpoint: opc.tcp://0.0.0.0:4840   # default opc.tcp://localhost:4840
```

`naut run` (and a built binary) starts the server alongside the scan loop;
the section's presence turns it on (`opcua: {}` serves on the default), and
`endpoint` is the only setting. It defaults to the loopback interface —
`localhost` listens on `127.0.0.1` — so putting a controller on the network
is a line you write: `opc.tcp://0.0.0.0:4840` listens on every interface.
`naut check` refuses an endpoint that is not `opc.tcp://host:port` (a bare
`host:port` is read as `opc.tcp://host:port`).

An OPC UA client finds an endpoint by its exact URL, so the server
advertises one per name it answers to: the host you wrote, every interface
address when you wrote `0.0.0.0`, the machine's host name, and loopback. A
client may dial any of them. One that asks about a URL the server never
heard of (a NAT address, a DNS alias) is given the whole list by
GetEndpoints, but the session itself has to be opened through one of the
advertised names: there is no way yet to advertise a name the host does
not know about ([#430](https://github.com/joyautomation/nautilus/issues/430)).

The server is **read-only**: a write is answered `Bad_UserAccessDenied`,
never silently dropped. See [Security](#security) for why, and what comes
next.

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
never produces — is sent as `Int64` rather than wrapped.

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
changes. `ServerTimestamp` is the time of the read.

Two limits of the current server, found by its black-box run and tracked
as issues: a driver input that has never been delivered has no node until
its first value arrives, so `Bad_NotConnected` cannot be observed and a
client that browsed early must browse again
([#420](https://github.com/joyautomation/nautilus/issues/420)); and a
change of quality alone, with the value unchanged, is visible to a Read but
not yet pushed to a subscription
([#419](https://github.com/joyautomation/nautilus/issues/419)).

## Subscriptions

A client may create subscriptions and monitor any Variable. The server
never polls a copy of the tag store: every 100 ms it asks the store which
tags changed since the last sweep — one integer compare per tag, using the
same write generations the HTTP stream uses — and notifies only the
monitored items whose value or status actually changed; a member of a UDT
is quiet while its siblings move. A subscriber hears of a change within
one sweep plus its own publishing interval; `MinimumSamplingInterval`
reports the sweep; a requested sampling interval is revised to the nearest
multiple of it, and the item is sampled at that rate.

An item that cannot be monitored — a node id that is not a node, or a
Value on an Object or the folder — is refused when it is created, with the
code a Read of it gives (`Bad_NodeIdUnknown`, `Bad_AttributeIdInvalid`),
not created and left to fail inside a notification.

## Security

The server offers the `None` security policy with anonymous sessions, and
every node is read-only (`AccessLevel` is `CurrentRead`) — the standard
`Server` object and its variables included, so no session can rewrite the
`NamespaceArray` or `ServerStatus` other clients rely on. That is the
combination that is safe to offer without a certificate store: anonymous
clients can look, nobody can write.

Writes are planned as a follow-up, and will land only on a secure channel
(`Basic256Sha256`) from a client certificate the controller trusts, going
through the same path as `POST /api/tags` — so the write token, forcing
rules and member-write checks the HTTP API applies hold for OPC UA too.
Until then, a controller that must take setpoints from a SCADA host should
take them over [Sparkplug](/guides/sparkplug/) or the HTTP API.

## Status

The dashboard's driver cards and `GET /api/drivers` carry an `opcua-server`
row: the endpoint, whether it is listening, and how many subscriptions and
monitored items clients hold. A port already in use is reported there and
on stderr at start (`continuing without it`); the controller runs
regardless.
