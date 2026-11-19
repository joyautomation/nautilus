---
title: Substitute values
description: Run the program on a chosen value while a sensor is bad or wrong, for a bounded time, flagged everywhere as substituted — declared in nautilus.yaml, started from the HMI or the API, kept across a restart and a takeover.
---

A **substitute** runs the program on a value someone chose while a sensor
is bad or wrong: a level transmitter being replaced, a flow meter reading
zero in a full pipe. It is the other half of [forcing](/guides/forcing/),
with the guard rails a held reading needs:

| | Force | Substitute |
|---|---|---|
| Time | until removed | **bounded**: it ends by itself, and a rule caps how long |
| Quality | the forced value reads Good | the tag reads **`substituted`** everywhere, and the alarm layer and the HMI say so |
| Who | whoever may write | a project **rule** names what may be substituted and on what value; starting and acknowledging are audited |
| Restart | lost | **kept**: a deploy must not put the bad reading back mid-run |
| Redundancy | this controller only | built to replicate through the operator-intent log (see [A redundant pair](#a-redundant-pair) for what is and is not wired yet) |

A substitute is for the logic to keep running. It is not a way to hide a
dead sensor: the live reading and its quality stay visible beside the
held value, and when time runs out the real value comes back.

## Declare what may be substituted

Rules go in the `substitutes:` block of `nautilus.yaml`, not in a tag
file: tag files are often generated (`naut plcnext import`, the IO-Link
profiles) and would lose them on the next import.

```yaml
substitutes:
  defaults: { max: 8h, role: supervisor }
  rules:
    - { match: Level, substitute: 95.0, max: 1h, ack-on-start: true }
    - { match: "LS_*", substitute: false, max: 30m }
    - { match: Flow, substitute: 1.0, max: 10m, operator-value: true }
```

| Key | Meaning |
|---|---|
| `match` | an **input** tag name or glob, or a member path (`IOL_P3.Level`). The first rule that matches decides |
| `substitute` | the value the program runs on |
| `max` | the longest grant, one minute to seven days (default 8 h) |
| `operator-value` | `true` lets the operator pick another value at the start; without it any other value is refused (`403`) |
| `ack-on-start` | the substitute must be acknowledged before the program's start interlock releases (below) |
| `role` | who may start one. Parsed and shown now; it is **not enforced** until roles land (#708), so every writer may start one today |

`naut check` holds each value to its tag's type, and reports how many
inputs are substitutable.

## Starting, stopping, acknowledging

From a screen, use the kit's `SubstitutesPanel` (see the
[HMI kit](/reference/hmi/)): what is held, the live
value and its quality beside it, who holds it, the time left, Stop, and a
start form built from the rules. From a script, the
[HTTP API](/guides/http-api/#substitute-values):

```sh
curl -s localhost:8080/api/substitutes | jq -c '.rules'
curl -s -X POST localhost:8080/api/substitutes \
  -d '{"name": "Flow", "value": 2.5, "seconds": 300, "requestId": "7b0e…"}'
curl -s -X POST localhost:8080/api/substitutes/ack -d '{"ids": [3]}'
curl -s -X DELETE localhost:8080/api/substitutes/3
```

Starting an address that already has a substitute replaces it with a new
entry and a new `id`; a substitute is never extended. Stopping puts the
real value back at once, not on the next delivery.

### Acknowledging at the start

With `ack-on-start: true` the program must not start the equipment on a
held reading by accident. The runtime owns a BOOL, `__SubstitutesUnacked`,
true while any such substitute waits; gate your start on it:

```iecst
Permit := NOT __SubstitutesUnacked;
```

Someone then acknowledges the substitute (`POST /api/substitutes/ack`, or
the panel's acknowledge bar), which is audited like the start.

## What the program and the screens see

Tags with a substitute read **`substituted`**. On the leader a substitute
on a whole tag wins over the driver, even over a driver's Bad: the program
runs on the held value, and the live reading and its quality are shown
beside it. For a member of a struct tag (`IOL_P3.Level`) a substitute
replaces only a member that reads Good; a member that is Bad, NotConnected
or Stale keeps that quality (worst first: Bad / NotConnected, then Stale,
then substituted, then Good). A [force](/guides/forcing/) still beats a
substitute. A standby controller reads a substituted input as **Stale**: it
is not the one running the plant.

The runtime owns these BOOL tags. They exist only in a project that has
substitutes:

| Tag | True while |
|---|---|
| `<tag>__Substituted` | that input (or a member of it) is substituted |
| `__SubstitutesUnacked` | an `ack-on-start` substitute is not yet acknowledged |
| `__Substitute_Expired` | pulses when a substitute ends by time: at least one main scan **and** one second, so the alarm engine and every sampler see it |
| `__Substitute_Fault` | a substitute needs a person (below). Not latched; the alarm layer latches |

`__Substitute_Fault` carries one of four reasons, on the entry or in the
journal: `clock-capped` (a takeover found the clocks further apart than
the skew allowance, below), `retain-load-failed`, `retain-save-failed` and
`catch-up-failed`. Every set and clear is a journal event
(`substitute.fault`, `substitute.fault-clear`) and a log line.

Alarm on the last two, and `naut check` knows these names are the
runtime's:

```yaml
alarms:
  defs:
    - { id: SubstituteExpired, tag: __Substitute_Expired, name: "A substituted value expired", priority: high }
    - { id: SubstituteFault, tag: __Substitute_Fault, name: "A substitute could not be kept", priority: high }
```

(`examples/bin-fill` has both.)

## How long it lasts

A substitute is granted a duration (≤ the rule's `max`). The controller
times it on a **monotonic** clock (`CLOCK_BOOTTIME`): setting the date or
an NTP step neither ends it early nor extends it. The wall-clock start and
end are recorded for people and for replication. Expiry is checked once per
main scan, before the logic, so the scan that sees the real value again
also sees `__Substitute_Expired`. Expiry ends the substitute, journals
`substitute.expire` by `system`, and restores the live value.

## A restart

A substitute survives a restart; the plant should not lose it in a deploy.

- The remainder is computed once at start-up from the recorded end on the
  wall clock, so **downtime counts**: a substitute whose time ran out while
  the controller was down expires at load, audited.
- If the wall clock went **backwards** across the restart, every entry
  expires at load rather than risk running past its grant. The controller
  records the wall clock (rounded down to 30 s) while a substitute runs, so
  a step back is seen; one smaller than the downtime plus 30 s is not, and
  can lengthen a substitute by up to that much.
- A clock step **while it runs** does not stretch it: each save re-anchors
  the end from the monotonic remainder.
- An entry the project no longer allows (its rule, input or type changed in
  a deploy) ends at load as an expiry with reason `refused-at-load`.
- **Rolling back to an older binary** (one from before substitutes) and
  forward again loses them: the older binary does not know the substitute
  block and drops it, entries and the highest id issued, at its next save.
  Ids then start again from zero, and any substitute that was running is
  gone.
- A retain store that cannot be read leaves the table as it is and raises
  `__Substitute_Fault` (`retain-load-failed`).

## A redundant pair

**Status: built and tested in process, not yet running in `naut run`.** The
server and runtime code below is exercised by tests that run three in-process
oplog nodes. `naut run` does not yet mount the oplog or the substitute log
(that is #710 PR 3), and it has not been run in the simulator or on hardware.
Until then a pair does not replicate substitutes; the single-controller
rules above are what runs.

The design: a start, stop or acknowledgement is **proposed to the
operator-intent log**, and every instance applies the committed entry, so
the standby holds exactly what the leader does. At a takeover the new
leader first **catches up the log, and applies every substitute, before its
first scan**: the logic never runs one scan on the bad reading. If the
catch-up fails it keeps what it retained and raises `__Substitute_Fault`
(`catch-up-failed`).

The remainder at a takeover is computed once from the recorded end on the
new leader's clock, then timed monotonically. Two controllers' clocks may
disagree by up to `SubstituteSkew` (30 s): a remainder within the granted
duration plus that is capped at the granted duration; beyond it the new
leader's clock was stepped, the entry is capped and `clock-capped` is
raised. Expiry is **leader-only** and is itself an entry in the log
(`unsubstitute`, reason `expired`), independent of quorum.

## When the answer is not certain

A write that has to be saved or committed can finish without the caller
knowing how. The API is explicit about it, and a screen must be too.

**Only an answer that carries `X-Nautilus-Outcome: refused` is certain**: the
server refused it before changing anything. Every other failure is
**unknown**: the `X-Nautilus-Outcome: unknown` 503, and also a 500, a 502, a
504 or a 503 without the header (a proxy, `tailscale serve` or a follower's
forward can return those *after* the leader applied the change), and a
connection that dropped. A start also has a crash case: a start is saved and
then applied, so a controller that dies between the two answers nothing, yet
the substitute is there after the restart. That is an unknown outcome too.

| Answer | Meaning |
|---|---|
| `200`, `kept: true` | applied and saved |
| `200`, `kept: false`, `warning` | **took effect, not saved** (a stop or an acknowledgement; a start that cannot be saved is refused): it may not survive a restart, or on a lost lease it may still be active on the next leader |
| `400`, `403`, `404` with `refused` | the request is invalid or no rule names the tag (400); a value the rule does not allow, or the permission (403); no such entry is active (404) |
| `401` | not authenticated. The substitute handlers send it with `refused`; a 401 from a proxy in front does not, and reads as unknown: sign in, then re-read |
| `409` with `refused` | the `requestId` was used for a different request, or a newer change won, or nothing was waiting for an acknowledgement |
| `501` with `refused` | this server mirrors a controller it does not run: substitute on that controller |
| `503` `no leader`, `refused` | this replica is standing by and no leader is known: nothing happened |
| `503` `operator-intent log has no majority`, `refused` | a redundant pair whose replicated log can't reach a majority (the other controller is down): nothing was proposed, nothing changed. Starts, stops and acknowledgements are refused until it returns. A write sent before the leader noticed the loss is answered **unknown** instead |
| `503` not saved, `refused` | the retain store refused a start: nothing was started |
| anything else | **unknown** |

What to do:

- **Unknown outcome of a start:** re-read `GET /api/substitutes`. If an entry
  with your `requestId` is there, it started. If not, retry **only with the
  same `requestId`** (a start retried with the same id is answered with the
  entry the first one began, never a second), or wait and re-read again. Never
  retry with a new id, and never start a second one on the same tag until it
  is settled.
- **Unknown outcome of a stop or an acknowledgement:** they carry no
  `requestId`. Re-read and decide from what the list shows.
- **`kept: false`:** it is in effect now but not saved. `__Substitute_Fault`
  says the save failed. Once the store is healthy (the fault clears), repeat
  the stop or acknowledgement so it is saved; until then it may undo itself
  after a restart, or be active on the next leader.
- **409:** re-read and decide again; never retry blind.

Send a fresh **`requestId`** with every start, one per user action, and
reuse it on every retry of that action. The same id used for a different
request is a `409`. It is remembered for 8 days. The kit's
`SubstituteClient` and `SubstituteSession` do all of this.

## Test it

`naut test` has three steps, run on the test's virtual time, so an expiry
is a step that spends it:

```yaml
tests:
  - name: run on the substitute, then the sensor is back
    steps:
      - given: {Level: 10.0}
        advance: 300ms
      - substitute: {tag: Level}
        ack-substitutes: all
        advance: 300ms
        expect: {Mirror: 95.0, Level__Substituted: true}
      - given: {Level: 12.0}
        advance: 61m
        expect: {Mirror: 12.0, Level__Substituted: false}
  - name: restore puts the field back at once
    steps:
      - substitute: {tag: Level}
      - restore: [Level]
```

## The audit trail

Each change leaves one journal entry through the audit seam:
`substitute.start`, `substitute.stop`, `substitute.ack` (by whoever did it),
`substitute.expire` (by `system`) and `substitute.fault`. A start and an
acknowledgement are elevated actions, refused while the journal cannot be
written; a stop is the way back to normal and is always allowed.
