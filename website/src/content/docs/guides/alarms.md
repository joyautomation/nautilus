---
title: Alarms
description: ISA-18.2 alarm state from BOOL tags — active list, acknowledge, shelve, journal — declared in nautilus.yaml, served at /api/alarms, and asserted in virtual time.
---

An alarm engine is a small thing when alarms are just tags. The bits are
already there: your logic compares a measurement to a limit every scan,
or an edge node publishes a `.HH` member it computed itself. What is
missing is the part a *person* needs — an active list, who acknowledged
what, a shelf that expires, and a record of what happened.

That is what `alarms:` adds. Declare it in `nautilus.yaml`, and the
controller grows five `/api/alarms*` routes, a counts-only summary on
every stream frame, and an alarm journal — with no engine, no ticker, and
no opinion of its own about your control logic. A project with no
`alarms:` section behaves exactly as it did before this existed.

## The shape

```yaml
# nautilus.yaml
alarms:
  site-from: "^([A-Za-z]+[0-9]+)_"   # RTU9_ZONE15_FIT_001 → site RTU9
  area-from: "^[A-Za-z]+[0-9]+_([A-Za-z]+[0-9]+)_"   # → area ZONE15

  defaults: { ack-required: true, auto-clear: true, shelve: true }
  shelve-times: [5m, 15m, 30m, 1h, 2h, 4h, 8h]
  journal: { keep: 500 }             # + optional sink: file | postgres
  notify:
    - log: true                      # events also land in the log stream

  rules:                             # generate definitions in bulk
    - match: { type: AnalogInput, member: HH }
      name: "{desc} High High"
      priority: high
      on-delay: 5m
      enable: "{site}__Online"
      class: process

  defs: []                           # or explicit entries, one per condition

alarm-files:                         # generated sets, in their own artifact
  - alarms/rtu9.yaml
```

`examples/lift-station` is this, complete and runnable — fourteen explicit
`defs:`, no rule expansion needed at that size:

```sh
naut check examples/lift-station         # validate the rules, offline
naut alarms list examples/lift-station   # see what they expanded to
naut test examples/lift-station          # the acceptance suite
naut run examples/lift-station           # dashboard + /api/alarms
```

`examples/remote-fleet/scada` is the `rules:` half of the same feature:
**one** rule (`match: { type: Pump, member: Fault }`) expands across every
pump on all three sites' shared `Pump` Template, with `enable:
"{site}__Online"` suppressing a dark site's alarms instead of leaving them
lit on their last-known reading.

## Rules versus definitions

A **definition** is one alarm: an id, a BOOL condition path, a priority,
and the operator policy. A **rule** generates definitions in bulk by
matching a struct type and a member.

The reason rules exist is arithmetic. A fleet is the same handful of UDTs
repeated across a few hundred instances — an analog input, a motor, a
valve — so a dozen `(type, member)` pairs account for the overwhelming
majority of a site's alarms. Writing them out is a generated file nobody
reads; writing the dozen rules is a page you can review.

```yaml
rules:
  - match: { type: AnalogInput, member: HH }   # every AnalogInput tag's HH bit
    priority: high
  - match: { tag: "*_YA" }                     # every standalone alarm contact
    priority: critical
```

`match:` takes `type`, `member` and `tag`, all globs. A rule **with** a
`member` matches struct members; a rule **without** one matches flat BOOL
tags — a struct tag is not itself a BOOL, so "which member" is never a
detail a rule can leave out. Rules are tried in file order and the **first
match wins**, and the whole expansion happens once, at load, never per
scan.

`id`, `name`, `site`, `area`, `display` and `enable` are templates over
`{tag} {member} {site} {area} {desc} {type} {path}`. An unknown
placeholder is a load error — `{sight}` failing the build beats `{sight}`
reaching an operator screen as literal braces.

The rest are ordinary definitions, one per condition, and they belong in
`alarm-files:` when a generator emits them:

```yaml
# alarms/rtu9.yaml — a bare YAML list, exactly like a tag file
- id: RTU9_ZONE15_INT_001_YA
  tag: RTU9_ZONE15_INT_001_YA     # a flat tag, or "tag.member"; BOOL, true = in alarm
  name: RTU 9 Zone 15 Intrusion
  priority: critical             # diagnostic|low|medium|high|critical
  class: security
  site: RTU9
  area: ZONE15
  on-delay: 5m
  enable: RTU9__Online
```

`alarm-files:` mirrors `tag-files:` in every respect, including the part
that matters: an id declared in two sources is an **error naming both**,
never last-wins. Last-wins reads fine on the day it is written and rots
silently the first time the generator that emitted one side changes.

`naut alarms list` dumps the whole expanded set (`-o yaml` gives YAML
a manifest could take back verbatim; `-count`, `-site` and `-priority`
narrow it). That is the deal: a dozen rules covering two thousand alarms
is a good trade only because you can see the two thousand.

## States

Six states, the ISA-18.2 ones. The two axes are *is the condition present*
and *has an operator seen it*, which is why return-to-normal is not the
end of an alarm's life.

| from | event | to |
| --- | --- | --- |
| Normal | condition true, held `on-delay` | **Unack-Active** |
| Unack-Active | ack | Ack-Active |
| Unack-Active | condition false, held `off-delay` | **Unack-RTN** |
| Ack-Active | condition false | Normal — or Unack-RTN with `auto-clear: false` |
| Unack-RTN | ack | Normal |
| Unack-RTN | condition true again | Unack-Active |
| any | shelve *until* | **Shelved** — prior state restored on expiry |
| any | `enable` false, or the tag is missing | **Suppressed** |

`ack-required: false` collapses Unack-Active → Ack-Active and Unack-RTN →
Normal on arrival: the annunciate-only contacts that have no operator
workflow at all. `auto-clear: false` latches — the alarm happened, and
clearing the field bit does not discharge the obligation to see it.

**A shelf always expires.** There is no permanent shelf, because a
permanently silenced alarm is indistinguishable from a broken one; and it
restores the state it interrupted rather than dropping to Normal, so
shelving is never a back-door acknowledgement.

**Suppressed is the answer to a dark site.** Host tags hold their last
values, so a site that dies with four alarms up keeps them up forever.
Point `enable:` at that node's online flag — `enable: "{site}__Online"` in
a rule — and the whole site moves to Suppressed instead. A definition
whose tag does not resolve at all (a site that has never birthed) is
Suppressed with a reason, never an error: one dark site must not fault the
engine.

## Ack is host state

Acknowledgement and shelf are **never written back to the edge**. An ack
is meaningless to a site that is offline, which is exactly when operators
ack most; writing it back would make it a control action subject to
store-and-forward ordering; and it would not survive the node's next
birth.

They persist through [retain](/guides/redundancy/) instead, beside the
operator setpoints, which buys redundancy correctness for free: a standby
re-reads the retain store on the leadership edge, so a failover cannot
resurrect four hundred acked alarms as unacked. Only ack and shelf
persist — active and RTN re-derive from the field, because a retained
"active" would be a claim about the plant made by a file.

An edge-side latch reset (`M.FAILRST`, a horn acknowledge) is a different
thing and stays an ordinary tag write. It is not the same button as
"Ack".

## The API

```
GET  /api/alarms                    {"ts":…,"summary":{…},"alarms":[…]}
GET  /api/alarms/journal?from&to&site&priority&id&kind&state&q&limit
POST /api/alarms/ack       {"ids":["RTU9_ZONE15_FIT_001.HH"],"by":"operator1"}
                           {"all":true,"by":"operator1"}
POST /api/alarms/shelve    {"id":"…","seconds":1800,"by":"operator1"}
POST /api/alarms/unshelve  {"id":"…","by":"operator1"}
```

Responses and status codes are in the
[HTTP API](/guides/http-api/#alarms) reference. Reads follow the same
Host rule as every other read. Writes go through the same
authorization as a tag write — `Authorization: Bearer` or
`X-Nautilus-Token`, same-origin by default — and a standby forwards them
to the leader exactly the way it forwards a tag write.

`by` is an **audit string the server does not authenticate**. Nautilus has
one token, not user accounts: the HMI supplies the operator's name and the
journal records what it was told. That is worth saying plainly rather than
implying an identity the system does not have.

Every stream frame gains one optional field:

```json
"alarms": { "active": 3, "unacked": 1, "shelved": 0, "suppressed": 12,
            "byPriority": {"high": 2, "medium": 1}, "worst": "high",
            "newest": {"id":"…","name":"…","priority":"high","ms":…},
            "rev": 41 }
```

Counts only — never two thousand rows on a 250 ms tick. `rev` bumps on any
state change, so an HMI refetches the full list exactly when something
moved and never otherwise. `GET /api/meta` reports `alarms: true|false`
and the offered `shelveTimes` (in seconds), so a screen knows whether to
render a banner at all instead of finding out by taking a 404.

With no `alarms:` section, all five routes answer **404** and the frame
carries no `alarms` field. That is deliberate: an empty 200 would look
like a quiet plant.

## The journal

Every state change produces exactly one event: `active`, `rtn`, `ack`,
`shelve`, `unshelve`, `suppress`, `unsuppress`. An escalation's sends are
journalled too, as `notify` rows (see [Escalation and on-call](#escalation-and-on-call)).

An in-memory ring is **always on**, in front of any sink, so the journal
page works on a box with no database at all — and it is bounded by
construction, which is the answer to a flapping storm across a couple of
thousand alarms. `journal: {keep: 5000}` sets its depth.

Two durable sinks are available:

```yaml
journal: { sink: file, path: alarms.jsonl }        # rotated JSONL
journal: { sink: postgres, dsn-env: NAUTILUS_ALARM_DB_URL }
```

`dsn-env` names the **environment variable** holding the connection
string, never the string itself: a manifest is committed, and a database
password in git is a password you have to rotate. It defaults to
`NAUTILUS_ALARM_DB_URL`, falling back to the historian's `DATABASE_URL`,
since `alarm_events` and `samples` belong in the same place. The table is
created idempotently at startup, the same one-DDL-string idiom the
historian uses.

`notify:` is the ship-it-somewhere seam — `- log: true` and
`- webhook: https://…` (with `header-env:` for its API key). Notifiers run
off the scan goroutine on a bounded queue: a slow endpoint drops events
with a counter rather than stalling the scan, because a controller that
waits on an HTTP call is a controller that stops controlling.

## Escalation and on-call

`notify:` ships every event. Escalation is different: it pages *people*
about an alarm nobody has acknowledged, level by level, until someone
does.

```yaml
alarms:
  oncall:
    timezone: America/Chicago        # rosters and windows read in this zone
    rosters:
      - { id: operators, members: [alice, dave] }          # everyone, always
      - id: standby                                        # one at a time
        members: [bob, erin]
        rotate: weekly                                     # or daily
        start: 2026-11-02                                  # bob's first week
        handover: "07:00"
      - id: crew                                           # by shift
        shifts:
          - { days: [mon-fri], from: "06:00", to: "18:00", members: [alice] }
          - { days: [mon-fri], from: "18:00", to: "06:00", members: [dave] }
  escalation:
    - id: process
      match: { class: process, priority: [high, critical] }  # also id, site, area
      # when: { days: [mon-fri], from: "06:00", to: "18:00" }  # optional
      levels:
        - { after: 0s,  notify: [roster:operators], via: [log] }
        - { after: 10m, notify: [roster:standby],   via: [log] }
        - { after: 30m, notify: [carol],            via: [log] }
      repeat: { every: 15m, times: 2 }   # the last level again
      stop: ack                          # or ack-or-rtn
```

- **People, not addresses.** A level names a roster (`roster:<id>`, read
  when the level is sent, so a handover between levels pages the new
  person) or a person by principal: a user name, or `oidc:<name>`. Email
  addresses and phone numbers never go in the manifest.
- **The first policy that covers the alarm runs it.** `match:` fields AND
  together; `when:` is read once, at the moment the alarm began waiting, and
  the policy then runs to the end even past the window.
- **The clock starts when the alarm begins waiting on a person,** not at
  its latest activation: a latched alarm that flaps between unack-active
  and unack-rtn keeps climbing. An acknowledgement, a shelf or a
  suppression ends the escalation; with `stop: ack-or-rtn`, so does a
  return to normal.
- **Only the controller holding the write gate pages.** The gate is asked
  immediately before every send, so a standby sends nothing, and a leader
  that loses its lease sends nothing more. A controller that wins the
  lease again with an escalation already under way sends the level now
  due, once, never a burst of the earlier ones; the journal says
  `joined late`. The escalation clock does not survive a restart or a
  takeover yet: an alarm still waiting there is a new wait, and starts
  again at level 1.
- **A roster is read once per level,** when that level falls due. A
  handover after it pages the new person at the next level, never with
  the levels already sent.
- **A failed send is not retried.** The channel's own retries apart, the
  next level, or a repeat, comes on its own timer: escalation never waits
  on delivery.
- **An alarm with `ack-required: false` never pages:** it never waits on a
  person, so nothing could stop the escalation. `naut alarms escalation`
  counts these apart, and `naut check` warns when a policy matches one.
- **Only `naut run` pages.** `naut test` and the acceptance harness build
  no escalation at all.
- **The browser playground has no time-zone database,** so there a
  project loads only with `timezone: UTC` (or none); `naut` itself has
  every IANA zone built in.

Every send is journalled as a `notify` row (level, the person, the
channel, and `sent`, `failed` or `nobody-on-call`), never with an
address. `GET /api/alarms/journal` leaves these rows out unless asked:
`?kind=notify`. They count against the in-memory ring's `keep` like any
other row. A channel whose last three sends all failed raises the system
alarm `nautilus.alarm-notify`, until a send on it succeeds (or this
controller stops leading).

This release has one channel, `log`, which writes each page to the log
stream: it is how a policy is tried before people are paged. Email, SMS,
voice and webhook channels, and acknowledging from a notification, come
next.

Check a policy offline:

```sh
naut alarms oncall -at "2026-11-23 06:55" .         # who each roster has on call
naut alarms escalation .                            # alarms covered, per policy
naut alarms escalation -id LSHH101 -at "2026-11-23 06:55" .   # the steps one alarm would page
```

## On the HMI

The kit ships three components and a client. Props in, callbacks out, no
fetching in the components:

| component | props |
| --- | --- |
| `AlarmBanner` | `summary`, `now`, `onclick?` — counts by priority, worst first, newest unacknowledged name |
| `AlarmTable` | `alarms`, `now`, `onack`, `onshelve`, `shelveTimes`, `sites?` |
| `AlarmJournal` | `events`, `from`, `to`, `onrange`, filters |

`createAlarmClient(realtime)` wires them up: it watches `frame.alarms.rev`
and refetches `/api/alarms` only when something moved, exposes
`active` / `shelved` / `summary` as runes, and flips `supported` to
`false` on that 404 so a controller without alarms renders nothing rather
than an error.

Priority never travels as colour alone — every one carries a label and a
glyph, and the palette reuses the theme's existing `--crit` / `--serious`
/ `--warn` / `--ink-2` / `--muted` roles.

## Testing

Alarm state is host state: it does not live in the tag store, and no ST
expression can see it. So acceptance tests get one key and three verbs of
their own, and virtual time makes an `on-delay` an exact assertion:

```yaml
- given: { RTU9_ZONE15_FIT_001.PV: 120.0 }
  advance: 4m                    # the bit is set; the ALARM is not, yet
  alarms: { active: [], unacked: 0 }
- advance: 1m1s                  # ... and now the on-delay has elapsed
  alarms:
    active: ["RTU9_ZONE15_FIT_001.HH"]
    unacked: 1
    state: { RTU9_ZONE15_FIT_001.HH: unack-active }
- ack: { ids: ["RTU9_ZONE15_FIT_001.HH"], by: test }
  scans: 1
  alarms: { state: { RTU9_ZONE15_FIT_001.HH: ack-active }, unacked: 0 }
- given: { RTU9_ZONE15_FIT_001.PV: 50.0 }
  scans: 1
  alarms: { active: [], journal: [active, ack, rtn] }
```

The matchers are `active`, `unacked`, `shelved`, `state`, `priority` and
`journal`; the verbs are `ack:`, `shelve:` and `unshelve:`. See
[Testing](/reference/testing/) for the full table.

## Checking it offline

`naut check` validates the whole section without running anything, and
it is where the mistakes that would otherwise wait for commissioning
surface:

- a condition path naming a member the type does not have, or one that is
  not a BOOL — an **error**, because the type is right there in the
  project and nothing at run time can make `.HHH` appear;
- a tag this manifest does not declare — a **warning**, since on a
  Sparkplug host most tags arrive from the field;
- a rule that matches nothing — a **warning**: dead, or a generator that
  moved out from under it.

It prints the definition count too, which is the number to diff against
whatever generated the tag set.

## Where the engine runs

**Bits at the edge, engine at the host.** Alarm *detection* is control
logic with a scan-rate guarantee; alarm *management* is an operator
concern that must survive the edge node going dark. On a Sparkplug fleet
that split is already true on the wire — a template lands as one struct
tag and the conditions are its members.

But the same engine runs on a single controller: point a definition at a
BOOL your own ladder computes and it works on a one-box `driver: memory`
project. That is the design's own smoke test, and it is what
`examples/lift-station` is.

Evaluation happens once per main scan, on the runtime's post-scan hook —
inside the scan lock, after outputs are written, when the tag store is
consistent and nothing else can be scanning. A standby skips `Scan()`
entirely, so a standby never evaluates: correct by construction.
