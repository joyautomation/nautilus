---
title: HTTP API
description: Every endpoint a running controller serves — method, request and response, status codes, the write token, and which tags a write may touch.
---

`naut run` and a controller built with `naut build` serve one HTTP API: the
live tag store, the scan diagnostics, the force table, alarms, and the
running programs. The built-in dashboard and the VS Code extension use it,
and so can `curl`. This page lists every endpoint.

The API listens on `server.addr` in `nautilus.yaml` (default
`localhost:8080`). The `NAUTILUS_ADDR` environment variable overrides it.

Every example below was captured from a running controller. The project is
a small lift station on the `memory` driver:

```yaml
name: lift-station
server:
  addr: "localhost:8080"
  online-edits: true
tasks:
  - program: program.st
    scan: 100ms
tags:
  - { name: Level,       role: input,    type: REAL, init: 0.0, unit: "%", desc: Wet well level }
  - { name: StartLevel,  role: setpoint, type: REAL, init: 70.0, unit: "%" }
  - { name: StopLevel,   role: setpoint, type: REAL, init: 20.0, unit: "%" }
  - { name: HighLevelSP, role: setpoint, type: REAL, init: 90.0, unit: "%" }
  - { name: Recipe,      role: setpoint, type: STRING, init: "normal" }
  - { name: Starts,      role: state,    type: INT, init: 0 }
  - { name: P101,        role: setpoint, type: PumpData, init: { Enable: true, Speed: 60.0 } }
  - { name: PumpRun,     role: output,   type: BOOL }
  - { name: HighLevel,   role: output,   type: BOOL }
alarms:
  defs:
    - { id: HighLevel, tag: HighLevel, name: Wet well high level, priority: high }
driver:
  type: memory
```

`PumpData` is a `STRUCT` with `Enable : BOOL` and `Speed : REAL`, declared
in a library file. Long arrays and the scan diagnostics block are trimmed
where marked `…`.

## Conventions

- **Bodies are JSON.** The server does not check `Content-Type`, so
  `curl -d '…'` works as it is.
- **Timestamps are epoch milliseconds** (`ts`, `sinceMs`, `activeMs`, …),
  unless a field says otherwise.
- **A write that succeeds** answers `204 No Content` (tag writes) or `200`
  with JSON (everything else).
- **Errors are plain text** (`text/plain`), one line giving the reason. The
  `/api/program*` endpoints are the exception: they answer
  `{"error": "…"}` as JSON.
- **An unknown `/api/` path, or a known path with the wrong method**, is a
  JSON `404`. There is no `405`:

  ```sh
  curl -s localhost:8080/api/tags
  ```
  ```json
  {"error":"no such API endpoint: GET /api/tags"}
  ```

- **CORS is open.** Every response carries
  `Access-Control-Allow-Origin: *`. An `OPTIONS` preflight answers `204`
  and allows the methods `GET, POST, PUT, DELETE, OPTIONS` and the headers
  `Content-Type, Authorization, X-Nautilus-Token`. A browser page on
  another origin can read freely. Whether it can write is decided by the
  next section.

## Authentication

Reads are never gated: every `GET` answers without credentials. Every
endpoint that changes something is a **write**:

| Write endpoints |
| --- |
| `POST /api/tags` |
| `POST /api/forces`, `DELETE /api/forces/{name}`, `POST /api/forces/clear` |
| `POST /api/sfc/step`, `POST /api/sfc/transition` |
| `POST /api/alarms/ack`, `POST /api/alarms/shelve`, `POST /api/alarms/unshelve` |
| `PUT /api/program`, `POST /api/program/rollback`, `POST /api/program/activate` |

A write is checked before its body is read, in one of two modes.

**No token (the default).** A write is allowed when the request has no
`Origin` header (`curl`, a script, another server) or when the `Origin`
host matches the request's `Host`, as it does for the built-in dashboard.
A browser page on another origin is refused:

```sh
curl -s -X POST localhost:8080/api/tags \
  -H 'Origin: http://evil.example' \
  -d '{"name": "StartLevel", "value": 75}'
```
```text
cross-origin writes require an auth token (start nautilus with one, e.g. NAUTILUS_TOKEN)
```

That is a `403`.

**With a token.** Start the controller with `NAUTILUS_TOKEN` set. The
token is never written in `nautilus.yaml`. Every write must then carry it
in one of two headers:

```sh
NAUTILUS_TOKEN=s3cret naut run .

curl -X POST localhost:8080/api/tags -H 'Authorization: Bearer s3cret' \
  -d '{"name": "StartLevel", "value": 72}'
curl -X POST localhost:8080/api/tags -H 'X-Nautilus-Token: s3cret' \
  -d '{"name": "StartLevel", "value": 72}'
```

Both answer `204`. A missing or wrong token is a `401`:

```text
missing or invalid auth token
```

With a valid token, a write is allowed from any origin. That is how an HMI
served from another host writes.

**Program endpoints need one more switch.** `PUT /api/program`,
`POST /api/program/rollback` and `POST /api/program/activate` are refused
with `403` unless the manifest sets `server.online-edits: true`. That
check comes before the token check. See [Online edits](/guides/online-edits/).

## Endpoints

| Method | Path | What it does |
| --- | --- | --- |
| `GET` | [`/api/state`](#get-apistate) | One snapshot: every tag, the scan diagnostics, quality, forces, alarm counts |
| `GET` | [`/api/stream`](#get-apistream) | The same snapshot as Server-Sent Events, every 250 ms |
| `GET` | [`/api/meta`](#get-apimeta) | Tag types, units and descriptions, the I/O lists, capability flags |
| `GET` | [`/api/drivers`](#get-apidrivers) | Field driver and publisher status |
| `GET` | [`/api/cluster`](#get-apicluster) | This replica's redundancy status |
| `POST` | [`/api/tags`](#post-apitags) | Write one tag or one member |
| `GET` | [`/api/forces`](#forces) | The force table |
| `POST` | [`/api/forces`](#forces) | Force a tag or member, or change a force |
| `DELETE` | [`/api/forces/{name}`](#forces) | Remove one force |
| `POST` | [`/api/forces/clear`](#forces) | Remove every force |
| `GET` | [`/api/sfc`](#sfc-commands) | Running SFC charts |
| `POST` | [`/api/sfc/step`](#sfc-commands) | Jump a chart to a step |
| `POST` | [`/api/sfc/transition`](#sfc-commands) | Fire a transition once |
| `GET` | [`/api/alarms`](#alarms) | Active alarms and counts |
| `GET` | [`/api/alarms/journal`](#alarms) | Alarm events |
| `POST` | [`/api/alarms/ack`](#alarms) | Acknowledge |
| `POST` | [`/api/alarms/shelve`](#alarms) | Shelve until a time |
| `POST` | [`/api/alarms/unshelve`](#alarms) | Unshelve |
| `GET` | [`/api/program`](#programs) | The running program source and status |
| `PUT` | [`/api/program`](#programs) | Warm-swap a new program source |
| `POST` | [`/api/program/rollback`](#programs) | Back to the previous source |
| `GET` | [`/api/program/history`](#programs) | The project's commit history |
| `POST` | [`/api/program/activate`](#programs) | Warm-swap to a commit's sources |
| `GET` | [`/api/history`, `/api/history/…`](#historian-proxy) | Forwarded to the historian |

Outside `/api/`, `GET /` is the built-in dashboard and `/assets/…` its logo
and fonts. With `server.hmi` set, your HMI build takes `/` and the
dashboard moves to `/_nautilus/`. See
[Serving the HMI from the controller](/guides/tag-model/#serving-the-hmi-from-the-controller).

## Reading

### `GET /api/state`

One frame: the whole tag store plus the scan loop's diagnostics.

| Query | Meaning |
| --- | --- |
| `tags=` | Comma-separated globs (`Level,P101`, `Tank*`, `*.Speed`). Only matching tags and quality entries are returned. At most 40 patterns. |

```sh
curl -s 'localhost:8080/api/state?tags=P101,Level'
```
```json
{
  "ts": 1793686140332,
  "scans": 67,
  "tags": { "Level": 0, "P101": { "Enable": true, "Speed": 60 } },
  "locals": { "wasRunning": false },
  "scan": { "count": 67, "targetMs": 100, "lastMs": 0.016142, "ioErrors": 0, "logicErrors": 0, "ioHealthy": true, … },
  "alarms": { "active": 0, "unacked": 0, "shelved": 0, "suppressed": 0, "byPriority": {}, "worst": "diagnostic", "rev": 0 }
}
```

| Field | Present | Meaning |
| --- | --- | --- |
| `ts` | always | When the frame was built |
| `scans` | always | Scans run so far |
| `tags` | always | Tag name → value. A struct tag is a nested object, an enumerated tag its member name, a `STRING` a string |
| `locals` | when the program has `VAR` state | Program locals, read-only. Not narrowed by `tags=` |
| `scan` | always | Scan timing and error counters. See [Scan timing and real-time](/guides/real-time/) |
| `drivers` | when a driver reports status | The same list as [`/api/drivers`](#get-apidrivers) |
| `quality` | when any tag is not good | Tag name → `stale`, `bad` or `notConnected`. Absent means every tag is good. See [quality](/guides/tag-model/#what-a-value-is-worth-quality) |
| `alarms` | when the project has `alarms:` | Counts only. See [Alarms](/guides/alarms/#the-api) |
| `forces` | while anything is forced | Forced address → forced value |

| Status | When |
| --- | --- |
| `200` | Always, unless one of the below |
| `400` | A `tags=` pattern does not compile (`bad tag pattern "[abc": syntax error in pattern`), or more than 40 patterns |
| `500` | A value could not be encoded as JSON |

### `GET /api/stream`

[Server-Sent Events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events).
There is no WebSocket. The first frame is sent at once, then one frame per
broadcast tick (250 ms by default, `server.interval` in the manifest). Each
event is one `data:` line holding a frame of the same shape as
`/api/state`:

```sh
curl -sN 'localhost:8080/api/stream?tags=Level,PumpRun'
```
```text
HTTP/1.1 200 OK
Access-Control-Allow-Origin: *
Cache-Control: no-cache
Connection: keep-alive
Content-Type: text/event-stream
X-Accel-Buffering: no

data: {"ts":1793686172342,"scans":387,"tags":{"Level":95,"PumpRun":true},"locals":{"wasRunning":true},"scan":{…},"alarms":{…},"forces":{"Level":95}}

data: {"ts":1793686172602,"scans":390,"tags":{"Level":95,"PumpRun":true},"locals":{"wasRunning":true},"scan":{…},"alarms":{…},"forces":{"Level":95}}
```

| Query | Meaning |
| --- | --- |
| `tags=` | Globs, as on `/api/state` |
| `delta=1` | After the first frame, send only the tags that changed |
| `blocks=delta` | With `delta=1`: send `scan`, `drivers` and `alarms` only when they change (`scan` every 3 s) |
| `full=1` | Never send deltas on this stream, whatever else is asked |

A delta stream adds `seq` (counts frames on this connection, from 1) and
`full: true` on a complete frame. These are the frames from
`?delta=1&blocks=delta&tags=StartLevel,Level` while `StartLevel` was
written from 80 to 82. The frames are reduced with `jq` to show which
blocks each one carried: `"…"` is a block that was present, `null` one
that was absent.

```sh
curl -sN 'localhost:8080/api/stream?delta=1&blocks=delta&tags=StartLevel,Level' \
  | sed -n 's/^data: //p' \
  | jq -c '{ts,scans,seq,full,tags,forces,scan:(if .scan then "…" else null end),alarms:(if .alarms then "…" else null end)}'
```
```json
{"ts":1793686179865,"scans":462,"seq":1,"full":true,"tags":{"Level":95,"StartLevel":80},"forces":{"Level":95},"scan":"…","alarms":"…"}
{"ts":1793686180102,"scans":465,"seq":2,"full":null,"tags":{},"forces":{"Level":95},"scan":"…","alarms":"…"}
{"ts":1793686180352,"scans":467,"seq":3,"full":null,"tags":{"StartLevel":82},"forces":{"Level":95},"scan":null,"alarms":null}
{"ts":1793686180603,"scans":470,"seq":4,"full":null,"tags":{},"forces":{"Level":95},"scan":null,"alarms":null}
```

`forces` rides every frame while anything is forced, delta or not. How to
merge deltas, when a full frame is resent, and what a gap in `seq` means
are in [Streaming to many clients](/guides/streaming/).

| Status | When |
| --- | --- |
| `200` | The stream opened |
| `400` | A bad `tags=` pattern, as on `/api/state` |

### `GET /api/meta`

What does not change scan to scan: each tag's declared type, unit and
description; which tags are driver inputs and outputs; and flags saying
which features this controller has.

```sh
curl -s localhost:8080/api/meta
```
```json
{
  "tags": {
    "HighLevel": { "type": "BOOL" },
    "HighLevelSP": { "unit": "%", "type": "REAL" },
    "Level": { "desc": "Wet well level", "unit": "%", "type": "REAL" },
    "P101": { "type": "PumpData" },
    "PumpRun": { "type": "BOOL" },
    "Recipe": { "type": "STRING" },
    "StartLevel": { "unit": "%", "type": "REAL" },
    "Starts": { "type": "INT" },
    "StopLevel": { "unit": "%", "type": "REAL" }
  },
  "locals": { "wasRunning": { "type": "BOOL" } },
  "inputs": ["Level"],
  "outputs": ["PumpRun", "HighLevel"],
  "scanTargetMs": 100,
  "memberWrites": true,
  "alarms": true,
  "quality": true,
  "deltas": true,
  "forces": true,
  "blockDeltas": true,
  "shelveTimes": [300, 900, 1800, 3600, 7200, 14400, 28800]
}
```

`inputs` is the list `POST /api/tags` refuses (see below). The flags say
whether this controller accepts member writes, runs an alarm engine,
reports quality, understands `delta=1` and `blocks=delta`, and holds a
force table. `shelveTimes` is in **seconds**. Always `200`.

### `GET /api/drivers`

Each field driver's and publisher's status. `[]` when the project has none
that reports (the `memory` driver does not). This one was captured from a
Modbus TCP variant of the project polling `naut modbus serve`:

```sh
curl -s localhost:8080/api/drivers
```
```json
[
  {
    "kind": "modbus",
    "name": "modbus",
    "detail": "1 source · 1 block",
    "state": "connected",
    "message": "Polling 1 source",
    "sinceMs": 1793686248567,
    "metrics": [
      { "label": "sources", "value": 1, "text": "1 / 1" },
      { "label": "blocks", "value": 1 },
      { "label": "reads", "value": 3 },
      { "label": "writes", "value": 0 },
      { "label": "errors", "value": 4 }
    ],
    "devices": [ { "id": "LT101", "online": true, "detail": "1 block" } ],
    "extra": {
      "sources": [
        { "id": "LT101", "addr": "127.0.0.1:15020", "state": "connected", "sinceMs": 1793686248567, "blocks": 1, "rttMs": 0.09304, "retries": 4 }
      ]
    },
    "asOfMs": 1793686250806
  }
]
```

`kind`, `name`, `detail`, `state` (`connected`, `connecting`, `waiting`,
`degraded`, `error` or `offline`), `message`, `sinceMs` and `asOfMs` are
common to every driver. `asOfMs` is stamped by the server: when this status
was read. `lastError` appears when there is one. `metrics`, `devices`
and `extra` are the driver's own; each protocol guide documents its
`/api/drivers` entry ([Modbus TCP](/guides/modbus/#apidrivers),
[EtherNet/IP](/guides/ethernet-ip/#apidrivers),
[Sparkplug host](/guides/sparkplug-host/#apidrivers)). Always `200`.

### `GET /api/cluster`

This replica's own view of [redundancy](/guides/redundancy/#redundancy).
Without redundancy it answers as a standalone leader:

```sh
curl -s localhost:8080/api/cluster
```
```json
{"mode":"standalone","pod":"","leader":"","isLeader":true,"transitions":0}
```

Always `200`, and always answered by the replica you asked, never
forwarded to the leader.

## `POST /api/tags`

Writes one tag, or one member of a struct tag.

```json
{ "name": "StartLevel", "value": 75 }
```

- `name` is a tag, or a member path into one: `P101.Speed`,
  `Recipe.Steps[2].Mode`.
- `value` is a JSON number or boolean. A string is accepted only by a
  `STRING` tag, an enumerated one (the member name), or a `TIME` (an IEC
  literal, see below). For a struct tag,
  `value` may be an object, which **merges**: the members it names are
  set, every other member keeps its current value.

```sh
curl -X POST localhost:8080/api/tags -d '{"name": "StartLevel", "value": 75}'
curl -X POST localhost:8080/api/tags -d '{"name": "P101.Speed", "value": 45.5}'
curl -X POST localhost:8080/api/tags -d '{"name": "P101", "value": {"Enable": false}}'
curl -X POST localhost:8080/api/tags -d '{"name": "Recipe", "value": "storm"}'
```

Each answers `204` with an empty body. Afterwards:

```sh
curl -s 'localhost:8080/api/state?tags=StartLevel,P101,Recipe' | jq -c .tags
```
```json
{"P101":{"Enable":false,"Speed":45.5},"Recipe":"storm","StartLevel":75}
```

A write never creates a tag, never changes a tag's type, and lands exactly
as sent or not at all. The full rules for members, arrays, enumerations and
integer widths are in
[Writing a member](/guides/tag-model/#writing-a-member-post-apitags-with-tagmember).

### `TIME` values

A `TIME` tag or member takes either a number of milliseconds or an IEC
literal as a string: `"T#1m"`, `"T#45s"`, a compound `"T#1m30s500ms"`, or a
bare `"30s"`. Reads always return milliseconds. These were captured from
a copy of the project with a `TIME` setpoint `RunDelay` and a `TIME` member
`P101.MinRun` added:

```sh
curl -X POST localhost:8080/api/tags -d '{"name": "RunDelay", "value": "T#1m30s500ms"}'
curl -X POST localhost:8080/api/tags -d '{"name": "P101.MinRun", "value": "T#2m"}'
curl -s 'localhost:8080/api/state?tags=RunDelay,P101' | jq -c .tags
```
```json
{"P101":{"Enable":true,"MinRun":120000,"Speed":60},"RunDelay":90500}
```

| Value | Status | Body |
| --- | --- | --- |
| a malformed literal, whole tag | `422` | `tag RunDelay: "T#5x" is not a TIME literal: not a duration (unit "x"; use d, h, m, s or ms) (write T#30s, T#1m30s, or a number of milliseconds)` |
| a malformed literal, member | `400` | `tag P101.MinRun: "T#5x" is not a TIME literal: …` (same text) |
| a literal finer than 1 ms | `400` | `tag RunDelay: "T#1.5ms" is finer than the millisecond a TIME holds` |
| a literal past the 64-bit range | `400` | `tag RunDelay: "T#999999999999999d" is longer than a TIME holds (T#-106751991167d7h12m55s808ms to T#106751991167d7h12m55s807ms)` |
| a fraction of a millisecond | `400` | `tag RunDelay: 2.5 is not a whole number of milliseconds a TIME can hold` |
| a number past the 64-bit range | `400` | `tag RunDelay: 1e+30 milliseconds is out of range for a TIME (T#-106751991167d7h12m55s808ms to T#106751991167d7h12m55s807ms)` |

A force takes the same values (`{"name": "RunDelay", "value": "T#45s"}`
forces `45000`), and refuses the same ones with `400`.

### Which tags may be written

The rule is applied to the **root** tag: writing `P101.Speed` is a write
to `P101`.

| Role | Writable | Notes |
| --- | --- | --- |
| `setpoint` | yes | What the API is for |
| `state` | yes | |
| `output` | yes | A program that assigns the output overwrites it on its next scan. Writing an output is how a [Sparkplug host](/guides/sparkplug-host/) commands an edge node |
| `input` | **no**, `400` | The driver owns it. To hold an input at a value, [force it](#forces) |
| not declared, or a `CONSTANT` | **no**, `400` | Nothing is created |

```sh
curl -s -X POST localhost:8080/api/tags -d '{"name": "Level", "value": 65}'
```
```text
tag Level is a driver-owned input: its value is the field's, so a write is refused — force it (POST /api/forces) to hold a value during bring-up, or write the setpoint or command tag the logic reads instead
```

A **forced** address refuses every write, whatever its role, with `409`.
That check comes first, so a forced input answers "forced", not "input".

### Status codes

| Status | When | Example body |
| --- | --- | --- |
| `204` | The value landed | (empty) |
| `400` | The body is not JSON, or has no `name` | `expected {"name": ..., "value": ...}` |
| `400` | The tag is not declared | `undefined tag Nope: not a declared tag, so there is nothing to write (a CONSTANT is not a tag and cannot be written)` |
| `400` | The tag is an input (whole, or a member of it) | see above |
| `400` | A member path names no member | `tag P101: unknown member Sped (did you mean Speed?)` |
| `400` | An integer outside its declared width | `tag Starts: 70000 is outside INT (-32768..32767)` |
| `400` | The wrong kind of value for a **member**, or for an enumerated tag | `tag P101.Enable: want BOOL, got a number` |
| `401` | A token is set and the request has none, or the wrong one | `missing or invalid auth token` |
| `403` | No token is set and a browser page on another origin wrote | `cross-origin writes require an auth token (start nautilus with one, e.g. NAUTILUS_TOKEN)` |
| `409` | The address, a member of it, or the tag that contains it is forced | `tag Level is forced — change the force value or remove the force first` |
| `422` | The wrong kind of value for a **whole** tag: a boolean to a `REAL`, a fraction to an `INT`, a string to anything but a `STRING`, an enumeration or a `TIME`, a malformed `TIME` literal, `null` | `tag StartLevel: want REAL, got a boolean` |

## Forces

A force holds a tag or a member at a value against the driver and the
logic until you remove it. This is how you drive an input. What a force
does, how long it lasts, and the VS Code view are in
[Forcing](/guides/forcing/). All four write calls need the same
authorization as `POST /api/tags`.

### `POST /api/forces`

```sh
curl -s -X POST localhost:8080/api/forces -d '{"name": "Level", "value": 85}'
```
```json
{"forces":[{"name":"Level","value":85,"sinceMs":1793686487789}]}
```

The response is the whole force table. Forcing an address that is already
forced changes its value. The input now reads the forced value, and the
frame says so:

```sh
curl -s 'localhost:8080/api/state?tags=Level,PumpRun,HighLevel' | jq -c '{tags,forces}'
```
```json
{"tags":{"HighLevel":false,"Level":85,"PumpRun":false},"forces":{"Level":85}}
```

`name` may be a member path. A write that overlaps a forced member is
refused, whole:

```sh
curl -s -X POST localhost:8080/api/forces -d '{"name": "P101.Speed", "value": 30}'
```
```json
{"forces":[{"name":"Level","value":85,"sinceMs":1793686487789},{"name":"P101.Speed","value":30,"sinceMs":1793686488210}]}
```
```sh
curl -s -X POST localhost:8080/api/tags -d '{"name": "P101", "value": {"Enable": true}}'
```
```text
tag P101.Speed is forced — change the force value or remove the force first
```

That is a `409`.

| Status | When | Example body |
| --- | --- | --- |
| `200` | Forced; the body is the force table | |
| `400` | No `name`, or no `value` (or `null`) | `expected {"name": ..., "value": ...}` |
| `400` | The tag does not exist | `undefined tag Nope` |
| `400` | The wrong kind of value | `tag StartLevel: want REAL, got a string` |
| `400` | A value the address cannot hold, by the same rules as `POST /api/tags` | `tag Starts: 70000 is outside INT (-32768..32767)` |
| `401`, `403` | Authorization, as for `POST /api/tags` | |

### `GET /api/forces`

```sh
curl -s localhost:8080/api/forces
```
```json
{"forces":[{"name":"Level","value":85,"sinceMs":1793686487789},{"name":"P101.Speed","value":30,"sinceMs":1793686488210}]}
```

Each entry may also carry `actual`: what the address would hold without
the force (the driver's latest delivery for an input, the logic's latest
write for an output). It is absent when nothing has been written since the
force, as here. `{"forces":[]}` when
nothing is forced. Always `200`.

### `DELETE /api/forces/{name}`

```sh
curl -s -X DELETE localhost:8080/api/forces/P101.Speed
```
```json
{"forces":[{"name":"Level","value":85,"sinceMs":1793686487789}]}
```

| Status | When | Example body |
| --- | --- | --- |
| `200` | Removed; the body is what is still forced | |
| `404` | That address was not forced | `P101.Speed is not forced` |
| `401`, `403` | Authorization | |

### `POST /api/forces/clear`

No body. Removes every force.

```sh
curl -s -X POST localhost:8080/api/forces/clear
```
```json
{"removed":1}
```

`200`, with `{"removed":0}` when nothing was forced; `401` or `403` on
authorization.

## SFC commands

Jump a running chart to a step, or fire a transition once. See
[Forcing: SFC](/guides/forcing/#sfc-set-a-step-fire-a-transition) for
what they do and how a transition is named.

| Call | Body |
| --- | --- |
| `GET /api/sfc` | none. Answers `{"charts":[…]}`: each chart's steps (`active`) and transitions (`enabled`). `{"charts":[]}` when no SFC program runs |
| `POST /api/sfc/step` | `{"step": "Fill", "pou": "Batch"}`. `pou` only when two charts share the step name |
| `POST /api/sfc/transition` | `{"transition": "Start", "pou": "Batch"}`. Same rule for `pou` |

| Status | When | Example body |
| --- | --- | --- |
| `200` | Done; the body is that chart's state, one entry of `GET /api/sfc` | |
| `400` | No `step` (or `transition`) in the body | `expected {"step": ..., "pou"?: ...}` |
| `404` | No SFC program is running, no program has that `pou`, no chart has that step or transition, or two charts do and `pou` was not given | `no SFC program is running` |
| `409` | The chart refused the jump or the firing | |
| `401`, `403` | Authorization | |

## Alarms

Present when the project has an `alarms:` section. The model (states,
shelving, the journal) is in [Alarms](/guides/alarms/). Without an
`alarms:` section, all five routes answer `404`:

```text
this controller has no alarm engine (add an `alarms:` section to nautilus.yaml)
```

On the three write routes, authorization is checked before that `404`.

### `GET /api/alarms`

Everything annunciating (active, unacknowledged after return to normal,
or shelved), worst first, and the counts. Forcing `Level` above
`HighLevelSP` raises the example's one alarm:

```sh
curl -s -X POST localhost:8080/api/forces -d '{"name": "Level", "value": 95}'
curl -s localhost:8080/api/alarms
```
```json
{
  "ts": 1793686488762,
  "summary": {
    "active": 1, "unacked": 1, "shelved": 0, "suppressed": 0,
    "byPriority": { "high": 1 }, "worst": "high",
    "newest": { "id": "HighLevel", "name": "Wet well high level", "priority": "high", "ms": 1793686488272 },
    "rev": 1
  },
  "alarms": [
    { "id": "HighLevel", "tag": "HighLevel", "name": "Wet well high level", "priority": "high",
      "ackRequired": true, "autoClear": true, "shelvable": true,
      "state": "unack-active", "cond": true, "activeMs": 1793686488272, "count": 1 }
  ]
}
```

Always `200` when the engine exists.

### `POST /api/alarms/ack`

```sh
curl -s -X POST localhost:8080/api/alarms/ack -d '{"ids": ["HighLevel"], "by": "operator1"}'
```
```json
{"acked":1}
```

`{"all": true, "by": "operator1"}` acknowledges everything. `by` is
recorded in the journal as given; the server does not authenticate it.

| Status | When | Example body |
| --- | --- | --- |
| `200` | `{"acked": n}`, the number acknowledged (`0` is not an error) | |
| `400` | Not JSON, or neither `ids` nor `"all": true` | `ack needs "ids" or "all": true — an empty ids list would silently ack nothing` |
| `404` | An id is not an alarm | `no alarm "NoSuch"` |

### `POST /api/alarms/shelve` and `/unshelve`

```sh
curl -s -X POST localhost:8080/api/alarms/shelve \
  -d '{"id": "HighLevel", "seconds": 1800, "by": "operator1"}'
```
```json
{"id":"HighLevel","tag":"HighLevel","name":"Wet well high level","priority":"high","ackRequired":true,"autoClear":true,"shelvable":true,"state":"shelved","cond":true,"activeMs":1793686488272,"ackMs":1793686488769,"shelfUntilMs":1793688288774,"ackBy":"operator1","shelfBy":"operator1","count":1}
```

Shelve takes `seconds`, or `until` (epoch milliseconds or RFC 3339). There
is no permanent shelf. Unshelve takes the id and `by`:

```sh
curl -s -X POST localhost:8080/api/alarms/unshelve -d '{"id": "HighLevel", "by": "operator1"}'
```
```json
{"id":"HighLevel","tag":"HighLevel","name":"Wet well high level","priority":"high","ackRequired":true,"autoClear":true,"shelvable":true,"state":"ack-active","cond":true,"activeMs":1793686488272,"ackMs":1793686488769,"ackBy":"operator1","count":1}
```

Both answer the alarm's record.

| Status | When | Example body |
| --- | --- | --- |
| `200` | The alarm's record | |
| `400` | No `id` (shelve) | `expected {"id": "...", "until"\|"seconds": ..., "by": "..."}` |
| `400` | No `id` (unshelve) | `expected {"id": "...", "by": "..."}` |
| `400` | Shelve with neither `until` nor `seconds` | `shelve needs "until" (RFC3339 or epoch ms) or "seconds" — there is no permanent shelf` |
| `400` | `until` unparseable, or not in the future | `alarm "HighLevel": shelve until 2020-01-01T00:00:00Z is not in the future` |
| `400` | Shelve of an alarm whose definition says `shelve: false` | `alarm "…" is not shelvable` |
| `400` | Unshelve of an alarm that is not shelved | `alarm "HighLevel" is not shelved` |
| `404` | The id is not an alarm | `no alarm "NoSuch"` |

### `GET /api/alarms/journal`

```sh
curl -s 'localhost:8080/api/alarms/journal?limit=3'
```
```json
{"events":[{"ts":1793686488778,"id":"HighLevel","name":"Wet well high level","kind":"unshelve","priority":"high","state":"ack-active","by":"operator1"},{"ts":1793686488774,"id":"HighLevel","name":"Wet well high level","kind":"shelve","priority":"high","state":"shelved","by":"operator1"},{"ts":1793686488769,"id":"HighLevel","name":"Wet well high level","kind":"ack","priority":"high","state":"ack-active","by":"operator1"}],"truncated":true}
```

Newest first. Query parameters: `from` and `to` (epoch milliseconds or RFC
3339; default the last 24 hours), `limit`, and comma-separated filters
`site`, `priority`, `id`, `kind`, `state`, plus `q` for a substring of the
name or id. `limit` defaults to 1000 (as does `0`).

`state` and `q` are applied before `limit`, so a page holds up to `limit`
matching events. `truncated` is `true` when more matching events exist in
the range than the page holds. One request reads at most 100,000 raw
journal rows looking for matches: past that it returns what it found with
`truncated: true` (narrow the range, or filter by `site`, `priority`, `id`
or `kind`), and a `limit` above 100,000 returns at most 100,000 events.

| Status | When | Example body |
| --- | --- | --- |
| `200` | The events | |
| `400` | Bad `from`, `to` or `limit` | `from: want epoch milliseconds or an RFC3339 timestamp` |
| `502` | The journal's durable sink failed the query | |

## Programs

The running program source, warm edits and rollbacks, and the project's
commit history. Reading is always open. The three writes need
`server.online-edits: true` and the write authorization. See
[Online edits](/guides/online-edits/) and
[Program history](/guides/program-history/).

### `GET /api/program`

```sh
curl -s localhost:8080/api/program | jq '.source |= (.[0:40]+"…")'
```
```json
{
  "task": "main",
  "pou": "LiftStation",
  "source": "TYPE PumpData :\nSTRUCT\n    Enable : BOOL…",
  "language": "st",
  "hash": "b983f85adadc",
  "dirty": false,
  "editable": true,
  "canRollback": false,
  "compiledAt": 1793686133601,
  "scans": 551,
  "programs": [
    { "task": "main", "pou": "LiftStation", "language": "st", "hash": "b983f85adadc", "dirty": false, "canRollback": false, "scans": 551 }
  ]
}
```

`source` is the composed source, libraries first. `?task=` or `?pou=`
selects a program other than the main task's. `programs` lists every task.
An unknown task or POU is a `404`: `{"error":"no task named nope"}`.

### `PUT /api/program`

```json
{ "source": "PROGRAM LiftStation\n…\nEND_PROGRAM", "baseHash": "b983f85adadc" }
```

The source is compiled and swapped in between scans, keeping retained
state. The target is chosen by `?task=` or `?pou=`, else by the
`PROGRAM` name in the source. `baseHash` is optional: when given and the
running program has changed since, the edit is refused.

```json
{"hash":"698a097c6056","task":"main"}
```

The response may also carry `resets`: variables that could not carry over
and restarted at their initial values.

| Status | When | Example body |
| --- | --- | --- |
| `200` | Swapped | |
| `400` | No `source` | `{"error":"body must be {\"source\": \"...\"}"}` |
| `403` | `online-edits` is off | `{"error":"online edits are disabled on this controller (set server.online-edits: true in nautilus.yaml)"}` |
| `404` | No such task or POU | `{"error":"no task named nope"}` |
| `409` | `baseHash` is not the running hash | `{"error":"controller program changed since your base — refresh and re-apply","hash":"b983f85adadc","task":"main"}` |
| `422` | The source does not compile; the old program keeps running | `{"error":"line 2: unexpected token \";\""}` |
| `401`, `403` | Authorization | |

### `POST /api/program/rollback`

No body; `?task=` or `?pou=` as above. Swaps back to the previous source:
`{"hash":"b983f85adadc"}`. `409` when there is nothing to roll back:
`{"error":"runtime: nothing to roll back"}`. Also `403` (online edits off),
`404` (no such task) and the authorization codes.

### `GET /api/program/history`

Every commit that touched the project, with its diff, and the running
programs. Always `200`; `commits` is `[]` when there is no history.

```sh
curl -s localhost:8080/api/program/history | jq '.commits[].diff |= "diff --git …"'
```
```json
{
  "built": "e230fc4933c086951a3562c0e1d707fd546e38e1",
  "editable": true,
  "commits": [
    { "sha": "e230fc4933c086951a3562c0e1d707fd546e38e1", "short": "e230fc4", "author": "A Developer",
      "date": "2026-11-02T22:08:51-08:00", "subject": "lift-station: pump control and high-level alarm",
      "diff": "diff --git …", "activatable": true }
  ],
  "programs": [
    { "task": "main", "pou": "LiftStation", "language": "st", "hash": "b983f85adadc", "dirty": false, "canRollback": false, "scans": 35 }
  ]
}
```

`builtDirty: true` appears when the build came from a tree with
uncommitted changes. `active` appears once a commit has been activated (see
below), holding its full sha, and is cleared by the next `PUT` or rollback.
`editable` is `true` when `online-edits` is on.

### `POST /api/program/activate`

```sh
curl -s -X POST localhost:8080/api/program/activate -d '{"sha":"e230fc4"}'
```
```json
{"programs":[{"task":"main","hash":"b983f85adadc"}],"sha":"e230fc4933c086951a3562c0e1d707fd546e38e1","short":"e230fc4"}
```

| Status | When | Example body |
| --- | --- | --- |
| `200` | Every program swapped | |
| `400` | No `sha` | `{"error":"body must be {\"sha\": \"\u003ccommit\u003e\"}"}` |
| `403` | `online-edits` is off | |
| `404` | No such commit in the history | `{"error":"no commit 0000000 in this controller's history"}` |
| `409` | The commit's sources cannot be rebuilt, or its tasks differ from the running ones | |
| `422` | A program at that commit does not compile; nothing was swapped | |
| `500` | A swap failed after every source compiled; the body's `programs` lists the tasks already swapped | |
| `503` | The server cannot rebuild sources from history. `naut run` and built controllers always can; an embedded Go server may not | |
| `401`, `403` | Authorization | |

## Historian proxy

`GET /api/history` and everything under `/api/history/` are forwarded to
the [historian](/guides/historian/#serving-history-to-the-hmi) at
`server.historian` (or `NAUTILUS_HISTORIAN_URL`), with `/api` removed:
`/api/history/agg?…` reaches `{historian}/history/agg?…`. The historian's
own status codes come back unchanged.

| Status | When | Example body |
| --- | --- | --- |
| `503` | No historian is configured | `no historian configured (set server.historian in the manifest, or NAUTILUS_HISTORIAN_URL)` |
| `500` | `server.historian` is not a valid URL | `bad historian url: …` |
| `502` | The historian did not answer | `historian unavailable: …` |

## On a standby replica

With [redundancy](/guides/redundancy/#redundancy), a standby forwards every
`/api/` request to the leader, except `GET /api/cluster` and
`/api/history*`, which it answers itself. Writes, forces and the stream
therefore always reach the controller that is scanning. When the leader
refuses the connection during a failover, the standby waits up to 10 s for
a new leader and sends the request there. Any other failure to reach the
leader is a `502` (`leader unavailable: …`).
