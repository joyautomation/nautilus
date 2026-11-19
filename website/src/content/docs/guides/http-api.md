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

- **CORS is for this machine and the controller's own names.** A browser
  page on another origin can read the API only when it cannot be someone
  else's site: a page on this machine (`http://localhost:5173`, any
  loopback address, any port), the controller's own address on another
  port (`http://192.168.1.10:3000` calling `192.168.1.10:8080`), a name in
  `NAUTILUS_ALLOWED_HOSTS`, or, with a token configured, a request that
  carries the token. For those the response echoes the page's origin in
  `Access-Control-Allow-Origin` (with `Vary: Origin`); every other site
  gets no CORS header, so its page cannot read the tags, the alarms, the
  history or the program source. An `OPTIONS` preflight answers `204` and,
  for the same origins (any origin when a token is configured), allows the
  methods `GET, POST, PUT, DELETE, OPTIONS` and the headers `Content-Type,
  Authorization, X-Nautilus-Token`. Whether a page can write is decided by
  the next section. A screen served by the controller itself, the HMI dev
  servers (their proxy presents the controller's origin), scripts and the
  VS Code extension need none of this. A screen served from another host
  must be at a name in `NAUTILUS_ALLOWED_HOSTS` to show live values: the
  live stream (`GET /api/stream`) is an `EventSource`, which cannot send a
  header, so the token cannot open it across origins. A token lets that
  screen's own `fetch` reads and writes through, not its stream. (There is
  deliberately no token in the query string: URLs end up in logs.)

## Authentication

Every request, read or write, is answered only when it is addressed to a
host no other site can take over, or, with a token configured, when it
carries the token (below). Every endpoint that changes something is a
**write**:

| Write endpoints |
| --- |
| `POST /api/tags` |
| `POST /api/forces`, `DELETE /api/forces/{name}`, `POST /api/forces/clear` |
| `POST /api/substitutes`, `DELETE /api/substitutes/{id}`, `POST /api/substitutes/ack` |
| `POST /api/sfc/step`, `POST /api/sfc/transition` |
| `POST /api/alarms/ack`, `POST /api/alarms/shelve`, `POST /api/alarms/unshelve` |
| `PUT /api/program`, `POST /api/program/rollback`, `POST /api/program/activate` |
| `POST /api/io/staged`, `POST /api/io/staged/{id}/validate`, `POST /api/io/staged/{id}/apply` |

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

**Every request must be addressed to a host no other site can take
over:** a read, a write, the live stream, the dashboard and the HMI alike,
token or not. A page on an attacker's domain can point that name at your
controller (DNS rebinding); it is then same-origin with your controller,
its `Host` and `Origin` are both the attacker's name, and no CORS rule
applies. So nautilus answers only a request addressed to `localhost` (or
a `*.localhost` name), to an IP address (`127.0.0.1`, `192.168.1.20`), or
to a name you list in `NAUTILUS_ALLOWED_HOSTS` (comma-separated, no port),
unless a token is configured and the request carries it:

```sh
NAUTILUS_ALLOWED_HOSTS=epic1.plant.lan naut run .
```

Any other name without the token is a `403` that says what to set, for
browsers, scripts and the CLI alike. That includes a controller reached
by its DNS or mDNS name (`epic1.local`), a Kubernetes Service name, and a
reverse proxy that passes the browser's host through: `tailscale serve`
keeps the `*.ts.net` name, nginx with `proxy_set_header Host $host` keeps
its public name. List the name, or send the token. A script can send it,
and so do `naut historian`, `naut pull --host` and the SNMP and Redfish
agents' `serve --from` (each from `NAUTILUS_TOKEN` in its environment), and
the VS Code extension (`nautilus.token`, only to `nautilus.runtimeUrl`; see
its setting); a browser loading the dashboard or an HMI
screen, and a live stream (`EventSource`), cannot send a header, so a
screen reached by a name needs that name listed, token or not. A CORS
preflight (`OPTIONS` with `Access-Control-Request-Method`) carries no
token, so on a token controller it is answered (`204`, no data); the
request it precedes still needs the token. When `naut run` listens beyond this machine
(`server.addr: ":8080"` or `0.0.0.0`) with no token, it starts and prints
a warning: anything that reaches the port can write.

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

Both answer `204`. A missing or wrong token is a `401` when the request
is addressed to a trusted host, and a `403` by any other name (the Host
rule above):

```text
missing or invalid auth token
```

With a valid token, a write is allowed from any origin. That is how an HMI
served from another host writes.

**Program and I/O configuration endpoints need one more switch.**
`PUT /api/program`, `POST /api/program/rollback`,
`POST /api/program/activate` and the `POST /api/io/…` routes are refused
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
| `GET` | [`/api/substitutes`](#substitute-values) | The active substitutes, and the rules that say what may be |
| `POST` | [`/api/substitutes`](#substitute-values) | Start a substitute value on an input |
| `DELETE` | [`/api/substitutes/{id}`](#substitute-values) | End one |
| `POST` | [`/api/substitutes/ack`](#substitute-values) | Acknowledge the ones that wait for it |
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
| `GET` | [`/api/io`](#live-io-configuration) | What I/O configuration runs, against the manifest |
| `POST` | [`/api/io/staged`](#live-io-configuration) | Stage a candidate I/O configuration |
| `POST` | [`/api/io/staged/{id}/validate`](#live-io-configuration) | Check it against what runs |
| `POST` | [`/api/io/staged/{id}/apply`](#live-io-configuration) | Apply it while the scan runs |
| `GET` | [`/api/io/staged/{id}`](#live-io-configuration) | One candidate's state |
| `GET` | [`/api/io/files`](#live-io-configuration) | The project files the running I/O configuration differs in (`naut pull --io`) |
| `GET` | [`/api/history`, `/api/history/…`](#historian-proxy) | Forwarded to the historian |
| `GET` | [`/api/audit`](#audit-journal) | Who changed what: the audit journal's recent entries |

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
| `quality` | when any tag is not good | Tag name → `stale`, `bad`, `notConnected` or `substituted`. Absent means every tag is good. See [quality](/guides/tag-model/#what-a-value-is-worth-quality) |
| `alarms` | when the project has `alarms:` | Counts only. See [Alarms](/guides/alarms/#the-api) |
| `forces` | while anything is forced | Forced address → forced value |
| `config` | on `naut run` and built controllers | What I/O configuration runs: `source` is `manifest`, or `applied` with the drivers and devices that differ listed under `differs`; `notKept` when it would not survive a restart; `discarded` for a kept change the last restart or takeover did not bring back. See [Live I/O configuration](#live-io-configuration) |

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
| `403` | No token is set and a browser page wrote by a host name that is not localhost, an IP address or in `NAUTILUS_ALLOWED_HOSTS` | `a browser write addressed to "plc.lan:8080" is refused without an auth token: …` |
| `403` | Any request (read or write) by a host name that is not localhost, an IP address or in `NAUTILUS_ALLOWED_HOSTS`, without the token | `this controller answers "plc.lan:8080" only with an auth token: …` |
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

## Substitute values

A substitute runs the program on a value the project chose while a sensor
is bad or wrong, flagged everywhere as quality `substituted`, for a
bounded time. Which inputs may be substituted, on what value, for how
long at most, is the manifest's `substitutes:` block. The three write
calls need the same authorization as `POST /api/tags`.

```sh
curl -s localhost:8080/api/substitutes | jq -c '.rules'
curl -s -X POST localhost:8080/api/substitutes -d '{"name": "LS_High"}'
curl -s -X POST localhost:8080/api/substitutes/ack -d '{"ids": [1]}'
curl -s -X DELETE localhost:8080/api/substitutes/1
```

- **`GET`** answers `{"substitutes": […], "rules": […]}`: each active entry
  with its `id`, `value`, the `live` value and `liveQuality` under it, who
  started it (`by`), `untilMs`, `remainingMs`, `ackOnStart` and `acked`,
  and its rule's `role`, `maxMs` and `operatorValue`.
- **`POST`** `{"name", "value"?, "seconds"?}` starts one. The value is the
  rule's unless the rule has `operator-value: true` (`403` otherwise);
  `seconds` defaults to the rule's maximum, may not exceed it, and is at
  least 1 (`400` otherwise). Starting
  an address that already has one replaces it with a new entry and a new
  `id`: a substitute is never extended. `400` for a tag no rule names.
- **`DELETE /api/substitutes/{id}`** ends that entry (`404` when it is
  not active: it expired, ended, or a newer one replaced it).
- **`POST /api/substitutes/ack`** `{"ids": […]}` acknowledges the entries
  that wait for it; `409` when none did.

Starting and acknowledging are elevated actions, refused while the audit
journal cannot be written; ending one is a way back to normal and is
allowed. An expiry has no request: it is journalled as `substitute.expire`
by `system`.

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
commit history. Reading needs only the Host rule (or the token) of
[Authentication](#authentication). The three writes need
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

## Live I/O configuration

A changed device address, unit id, register address, OPC UA endpoint or
node id applies while the scan runs: no restart, and only the changed
device's tags read anything but good during the swap. A candidate goes
through four visible steps: **pending** (staged), **valid** or **invalid**
(checked against what runs), then **applied** or **failed**. A refused
apply changes nothing. A failed one puts back what it had changed, so what
runs is as it was; only if putting it back fails too does the apply answer
`500`, with `config.fault` naming what is left in an unknown state until
an apply re-establishes it.

```sh
# stage: from a commit in the controller's history, or uploaded files
curl -s -X POST localhost:8080/api/io/staged -d '{"sha":"e230fc4"}'
curl -s -X POST localhost:8080/api/io/staged \
  -d '{"driver":"modbus","files":{"modbus_manifest.yaml":"sources: …"}}'
# → {"id":"1","state":"pending","from":"upload",…}

curl -s -X POST localhost:8080/api/io/staged/1/validate
# → {"id":"1","state":"valid","validation":{"valid":true,"checks":[…],
#    "changes":[{"driver":"modbus","device":"DEV_A",…}],
#    "willCommand":[{"tag":"A_Cmd","value":7}],"notes":[…]}}

curl -s -X POST localhost:8080/api/io/staged/1/apply
# → {"id":"1","state":"applied","result":{"changes":[…],
#    "commanded":[{"tag":"A_Cmd","value":7}],"notes":[…]}}
```

- **A commit** needs the controller's file snapshot of it (a controller
  built with `naut build` in a git repo, or `naut run` from one).
  **Uploaded files** replace the project's files of the same path; with
  `driver`, the candidate may change only that driver.
- **What may change** is addressing:
  - a Modbus device's `host`, `port` and `unitid`;
  - a binding's `table`, `address` and scan class;
  - an OPC UA client's `endpoint` and a binding's `nodeid` and
    `namespace`;
  - a driver's scan rate and classes;
  - a `devices:` row's `endpoint` and `unit-id`.
  
  Anything else is refused by name, with "restart to apply the rest":
  the tag set, the drivers and devices, a device's profile or points,
  word order, timeouts, keep-alives, scaling, security.
- **The unit applied** is one Modbus device (a `devices:` row is one),
  inside the driver that runs. Anything else swaps the whole driver; that
  works for `modbus` and `opcua-client` drivers, in a project with two or
  more drivers. A Modbus device applied on its own disconnects before it
  reconnects at its new address. A whole-driver swap starts the new driver
  before it stops the old one, so for a moment both are connected: a
  device that allows a single connection may refuse the new one until the
  old has gone.
- **`willCommand`** lists the outputs the first scan after the apply will
  send to what changes, with their values now (they can change before the
  apply). The apply's `commanded` is what that scan actually queued for
  the device: nothing while the write gate is closed (a standby, or scans
  not live), and a device parked by its enable tag holds its commands
  until it is enabled.
- **Live check.** Once a candidate passes every static check, each
  changed device is probed before the apply: a driver for that device
  alone, built as the apply would build it, is started on a connection of
  its own until every tag has read once (at most 5 s). It never writes.
  A device that refuses the connection, or a tag that does not read (a
  wrong unit id, a register or node the device does not have), makes the
  candidate invalid, named under `validation.probes` and the `live` check.
- **Live or static.** A device that allows few connections cannot spare
  one for the check, so how it is checked is, in order: its `devices:`
  row's `validate:`, its driver's `validate:`, then its profile's default
  (`static` when the profile documents Modbus `max-connections` of 2 or
  fewer, `live` otherwise). A static check reads nothing from the device.
  Each probe says which mode it ran and why. A failed connection says so,
  and suggests `validate: static`: the device may allow only one
  connection.
- **After the apply** the response comes at once: `answered` lists the
  changed devices that had read by then, `pending` the rest, and `watch`
  follows them from there, off the apply (another apply is not held up):
  `GET /api/io` shows it, and the frame's `config.watch` while it is
  still watching. A device still silent at the deadline (5 s) stays
  applied, reads bad quality and alarms, and the operator decides
  (`watch.state` `silent`). To have it put back instead, ask for it:
  `POST /api/io/staged/{id}/apply` with `{"revertIfSilent": "10s"}` (up
  to 5 min) reverts the whole apply if a changed device has not read by
  then (`reverted`, with `watch.reason` naming it), unless a newer apply
  has landed meanwhile (`superseded`) or the revert was cancelled:
  `POST /api/io/revert/cancel` (`cancelled`). A client that goes away
  changes nothing: the watch is the controller's. Off by default: a
  reversion on a running plant can be its own surprise.
- **Kept across a restart and a takeover.** With a `retain:` block, an
  applied configuration is saved in the retain store before the apply
  answers (`kept: true`). A restart, or a standby taking over, re-applies
  it before the first scan, so no logic runs and no output goes out
  on the old addresses. The outputs then go out as they would without
  the apply: a restart's first scan is the start-up baseline, and a
  takeover sends the full set. A standby follows what the leader applied
  while it stands by, so its takeover re-applies nothing and takes no
  longer than before. The rules:
  - **A deploy wins.** A kept change comes back only while the deployed
    manifest still says what it was applied over. A deploy that changed
    that driver or device replaces it. The dropped change is listed under
    `config.discarded` (and kept in the store for recovery by hand). A
    deploy that contains the change clears it.
  - **A pending revert is not carried.** An apply with `revertIfSilent`
    is kept only once its devices answer, or its revert is cancelled. A
    restart or takeover before that comes back on the configuration
    before it, and `config.discarded` says so.
  - **A change that cannot be re-applied is held.** The device (or
    driver) is not polled, its tags read Bad, and it is sent nothing,
    until an apply re-establishes what runs. `config.fault` says why. An
    apply is refused while a restart or takeover is still re-applying.
  - **A save that fails.** On a project with `redundancy:` the apply is
    put back (`503`); a takeover would otherwise undo it. Standalone, it
    stays applied with `kept: false` and `keptWhy`, and the save is
    retried.
  - **No `retain:` block.** Nothing is kept, and a restart runs the
    manifest again. The apply answers `kept: false`. An apply on a
    project with `redundancy:` and no `retain:` block is refused (check
    `kept`).
  - **Not while a pair runs mixed versions.** A controller built before
    this ignores what is kept: if it takes over, it runs the manifest's
    addresses and its next save clears what was kept. During a rolling
    upgrade of a redundant pair, make no live I/O apply until both
    replicas run the new build.

  While what runs differs from the manifest, `config` on every frame
  says so (and `config.notKept` says if it would not survive a restart),
  and the built-in dashboard shows it, as does each driver's row on
  `/api/drivers` (`differs: true`). Commit the change to make it
  permanent.
- **Pulling it back.** `GET /api/io/files` returns, for every unit that
  differs, where it came from (`units`) and the project files it was read
  from that differ from the ones the controller booted from: per file its
  `path`, the bytes that `running`, and the sha-256 of the `booted` file
  (absent when the change added the file). `leading` is false on a standby.
  `conflicts` lists a path held with two different contents. `nonIO` is
  true when the applied manifest also changes sections an apply does not
  apply, which never ran. `naut pull --host <controller> --io` writes
  those files into `--dir`. It refuses and names a file you edited locally
  since the build unless `--force`, and it writes nothing at all if it
  refuses one. `--dry-run` prints a unified diff and writes nothing;
  `--check` writes nothing and exits non-zero while anything differs. A
  standby is refused: ask the leader, or the service address.

| Status | When |
| --- | --- |
| `201` | Staged |
| `200` | Validated (valid or invalid: see `state`), applied, or read |
| `400` | A body with neither or both of `sha` and `files`; a `revertIfSilent` that is not a duration up to 5 min |
| `403` | `online-edits` is off |
| `404` | No such candidate |
| `409` | No snapshot of the commit; applying a candidate that is not valid; another apply is in progress, or what runs changed since it was validated (validate again) |
| `422` | The candidate does not load, or an apply failed and was put back: nothing changed |
| `500` | An apply failed and putting it back failed too; `config.fault` names what is left |
| `503` | No live I/O configuration on this server (an embedded Go server); or, on a project with `redundancy:`, the retain store could not keep the apply, so it was put back |
| `401`, `403` | Authorization |

## Audit journal

Every request to a route above that changes something (`POST /api/tags`,
the force, SFC, alarm, program and live I/O routes) leaves **exactly one** audit
entry, accepted or refused, and is answered only after the entry is
recorded. `naut run` writes the entries to a hash-chained journal on disk
(the manifest's `audit:` section, default `./audit`), which
`naut audit verify` checks.

Each entry has:
- `user`: `token` when the request carried the auth token, else `anonymous`.
- `source`: `X-Nautilus-Client` (`hmi`, `ide`, `cli` or `api`) when the client sends it, else `hmi` for a browser and `api` for anything else.
- `action` (`tag.write`, `force.set`, `alarm.ack`, `program.edit`, …), `target`, and `before` and `after` values.
- `result`: `ok`, `refused` (with `detail.reason`) or `error`.
- `from`: the client's IP address.

An online edit is journalled as program hashes, never the source. A
request refused for want of the token (or from a cross-origin page) is
journalled without reading its body: who, from where and which action,
with no target or values. Otherwise the target is capped at 1 KiB, and
`before` and `after` at 64 KiB of JSON (4 KiB on a refusal); a larger
value is journalled as `{"truncated": true, "bytes": …, "sha256": …}`.
Refusals and errors from one address are coalesced: five a minute in full
per action, then one entry counting the rest (`detail.coalesced`). A write
body over 16 MiB is refused with `413`. On a redundant pair, a write is
journalled by the leader that applies it; one a standby answers itself
(`503` while it holds the lease with its scans stalled, `502` when the
leader cannot be reached) is journalled by the standby.

### When the journal cannot be written

Setting a force or changing the program must never happen unrecorded, so
while the journal cannot be written, or `naut run` could not open it (an
unwritable `audit.path`, or another `naut run` already using it):

- **Elevated actions are refused** with `503`: setting a force, a program
  edit or activation, staging, validating or applying a live I/O
  configuration, starting or acknowledging a substitute value, and any
  action not listed below. The refusal is
  journalled, in memory.
- **Operator actions are allowed**: tag writes, alarm acknowledgement and
  shelving, SFC commands.
- **The ways back to normal are allowed**: clearing a force or all of
  them, ending a substitute value, and rolling a program edit back.
- An allowed action's response carries `X-Nautilus-Audit-Journal: down`,
  and its entry is kept in memory (up to 10,000; past that the oldest are
  counted in an `audit.dropped` entry) and written, with its id, once the
  journal recovers. A journal that could not be opened keeps only the last
  1000, lost on restart.
- **It is shown:** an error is logged, an `audit` card with state `error`
  appears on the drivers block (`GET /api/drivers`, `/api/state`, the live
  stream), and, when the project has alarms, the system alarm
  `nautilus.audit-journal` (high priority, not shelvable) is raised. Both
  clear when the journal recovers; one that could not be opened needs a
  restart.

The action that first finds the journal broken has already been carried
out (see the limits below); elevated actions are refused from the next one.


Known limits:

- The action takes effect before its entry is written: a crash
  between the two leaves an applied action without an entry, and a write
  that fails leaves one entry in memory only until the journal recovers.
- A write a standby forwarded, which the leader applied before the
  connection failed, has two entries: `ok` on the leader and a `502` on the
  standby.
- `GET /api/audit` is open when no auth token is set, as every read is
  today. With users and roles (#708) it needs a signed-in principal with
  the `audit` permission.
- The program routes `naut logix serve` answers itself (`PUT /api/program`,
  which edits the Logix project, and `POST /api/logix/deploy`) are not
  journalled yet, nor are the refusals of the program routes on a Siemens
  facade.

### `GET /api/audit`

The last 1000 entries, newest first:

```json
{"entries": [{"id": "01J…", "seq": 42, "ts": "2026-11-18T14:02:11.204Z",
  "user": "token", "source": "hmi", "action": "tag.write", "target": "SP",
  "before": 65, "after": 70, "result": "ok", "from": "192.0.2.22",
  "prev": "sha256:…"}], "truncated": false}
```

Query parameters filter it: `from` and `to` (epoch ms or RFC 3339),
`user`, `action`, `target` and `result` (exact, ignoring case), and
`limit` (default 100). `truncated` is true when more entries matched.

| Status | When |
| --- | --- |
| `401` | An auth token is set and the request does not carry it: the entries say who did what from where. With no token set it is open, like every read, until users and roles (#708) |
| `400` | A bad `from`, `to` or `limit` |

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
