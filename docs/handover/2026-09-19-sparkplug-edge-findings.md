# Handover: Sparkplug edge-node findings from the Mantle for Ignition integration tests

**Date:** 2026-09-19 · **Against:** `fffc3a5` (branch `diff-revisions`) · **Scope:** `sparkplug/` (the edge node), plus
one item in `internal/project/`

`~/Development/joyautomation/ignition` now has an integration suite that runs a real Nautilus edge node against an
Ignition gateway (`integration/` there; `go test ./...`, ~90 s). It was written to test the Ignition module, and it
found three things on this side of the wire. One is a bug that stops a node publishing for good. Two are gaps.

Nothing in this repo has been changed except adding this file and `scripts/repro-sparkplug-silent-link.sh`.

---

## 1. BUG: after a keepalive timeout the node reconnects, rebirths, and never publishes data again

**Severity: high.** This is the radio-link / hung-controller case, which is the normal failure in the field. The
node looks healthy from every angle (`born=true`, status "Publishing", dashboard fine, host shows it online after
the rebirth) and delivers nothing until the process is restarted. With store-and-forward on, nothing is buffered
either, because the publish loop isn't running.

### Reproduce (70 s, no Ignition, no primary host)

```sh
scripts/repro-sparkplug-silent-link.sh        # needs a broker on localhost:1883 and mosquitto_sub
# or borrow the ignition dev stack's broker:
MOSQ="docker compose -f ../ignition/docker-compose.yml exec -T broker" scripts/repro-sparkplug-silent-link.sh
```

It builds `./cmd/naut`, runs a two-tag project, freezes it with `SIGSTOP` until the broker times out the 30 s
keepalive, thaws it, changes a tag three times, and counts NDATA on the wire. Today:

```
after:  state=connected born=True messages=1 seq=0 | Publishing · bdSeq 0
edge's own LevelFt: 58
--- messages on the wire after the thaw (three tag changes were made):
      1 NBIRTH/silent-link
FAIL: 0 NDATA after the reconnect (want 3)
```

`SIGSTOP` is a faithful stand-in: the socket stays open and nothing arrives. Closing the socket (killing the
process, restarting the broker) does **not** reproduce it, which is why the existing tests and the TCK never saw it.
The ignition suite's `TestABrokerRestartIsSurvived` passes; `TestASilentNode…` is the one that fails.

### Root cause (confirmed, not inferred)

The script ends by sending `SIGQUIT` for a goroutine dump. The node's single publish goroutine is parked forever:

```
goroutine 47 [chan receive]:
paho.mqtt.golang.(*baseToken).Wait(...)
paho.mqtt.golang.(*PublishToken).Wait(...)
nautilus/sparkplug.(*Node).scanAndPublish   sparkplug/data.go:59
nautilus/sparkplug.(*Node).run              sparkplug/node.go:306
```

`data.go:59` is `n.cli.Publish(p.topic, 0, false, p.payload).Wait()`. On thaw, the ticker fires before paho has
noticed the connection is dead. `born` is still true (the connection-lost handler hasn't run yet), and after 50 s of
silence every metric's `max-interval` is due, so there is always something to publish. That publish is handed to a
connection that is about to be torn down, and paho (v1.5.1) never completes its token. `Wait()` has no timeout, so
`run()` never reaches its next tick. paho then reconnects and `onConnect` births from paho's own goroutine, which is
why the NBIRTH appears and everything looks alive.

**It is a race, and it usually but not always fires.** It needs a publish to be in flight in the instant after the
process wakes and before paho notices the connection is dead. If paho's connection-lost handler wins, `born` goes
false first, nothing is published, and the node recovers normally. Observed: wedged 6 runs out of 6 on a
workstation, then recovered cleanly on the 7th (the ignition suite against `main` @ `ee970eb`, identical
`sparkplug/` code). So **a single passing run of the repro script proves nothing**. The proof of a fix is the unit
test that forces the never-completing token, plus the repro passing several times in a row.

An earlier guess, recorded here so nobody re-walks it: this is **not** the primary-host gate.
`hostDeliverableLocked()` is fine, `hostOnline` is never reset on connection loss, and the bug reproduces with no
`primary-host` configured at all.

### The same hazard exists at every unbounded token wait in the edge package

```
sparkplug/birth.go:96     NBIRTH    tok.Wait()
sparkplug/birth.go:100    DBIRTH    .Wait()
sparkplug/data.go:59      N/DDATA   .Wait()      <- the one in the stack above
sparkplug/data.go:163     store-and-forward drain   .Wait()
sparkplug/data.go:289     DBIRTH (device health)    .Wait()
sparkplug/data.go:301     DDEATH (device health)    .Wait()
sparkplug/node.go:218     Connect   tok.Wait()
sparkplug/node.go:250     NDEATH in Stop()          .Wait()
```

`birth.go` is called from paho's `onConnect` goroutine and from `handleState`; a wedge there would block a paho
callback. `node.go:250` can hang `Stop()`, and with it a clean shutdown.

The host package already does this the right way: `sparkplug/host/mqtt.go` and `observe.go` use
`tok.WaitTimeout(tokenTimeout)` throughout. The edge package predates that and was never brought in line.

### What a fix needs

1. Every token wait in `sparkplug/` bounded, in the style of `sparkplug/host`. A QoS 0 publish that times out is a
   dropped sample, not an error worth stopping for.
2. Decide what a timed-out DATA publish means for the data. The honest options are: drop it (it was QoS 0), or
   treat the tick as undeliverable and hand its messages to store-and-forward when that is enabled, so the gap is
   replayed as historical after the rebirth. The second matches what `store-forward` promises. Whichever is chosen,
   `seq` must not advance for messages that were not sent, or the host sees a gap and asks for a rebirth.
3. `scanAndPublish` should not try to publish at all when the client isn't connected
   (`n.cli.IsConnectionOpen()`); today it relies on `born`, which lags the connection state by exactly the window
   that causes this.
4. A regression test. There is no reconnect test in `sparkplug/*_test.go` today. The in-process broker used by
   `sparkplug/host` (`startBroker` in `sparkplug/host/mqtt_test.go`, mochi-mqtt) is the natural tool, but note that
   stopping it closes sockets, which does **not** reproduce this. The test has to make a publish token that never
   completes: a TCP proxy in front of the broker that stops forwarding without closing, or a fake `mqtt.Client`
   whose `Publish` returns a token that never finishes. The second is simpler and tests the real invariant: *one
   publish that never completes must not stop the next tick.*
5. `scripts/repro-sparkplug-silent-link.sh` exits 0 when fixed, but because this is a race it can also exit 0 on
   broken code. Run it at least five times. Keep it; it is the end-to-end check.

---

## 2. GAP: a manifest can't declare an integer scalar tag

`internal/project/project.go`, `normalize()` (about line 782) turns every YAML integer into `float64` "because ST
REAL tags want the latter". So `{ name: Counter, role: state, init: 0 }` is seeded as a REAL even when the program
declares `Counter : DINT`, and its **NBIRTH says Double**. Once the program assigns it, the store holds an integer,
so the node births a metric as one type and may report it as another.

Struct members are fine: `ir.SeedFromInit` types them from the `TYPE` declaration. `type:` on a tag is only accepted
for UDTs today (`tag Counter: no TYPE DINT is declared by this project's ST`).

Seen from a host this is a wrong datatype on every integer tag of every manifest project: Ignition creates `Float8`
tags for counters and mode words. Options, roughly in order of how little they disturb: resolve a scalar tag's type
from the `VAR_EXTERNAL` declarations of the programs that use it (the information is already in the compile);
or accept IEC elementary type names in `type:` (`DINT`, `INT`, `REAL`, `BOOL`, `STRING`). The first needs no manifest
changes and fixes existing projects.

---

## 3. GAP: `unit` and `desc` are not published as Sparkplug metric properties

Tags carry `Meta.Unit` and `Meta.Desc` (`runtime/runtime.go:119`), and the dashboard and `/api/meta` serve them, but
the edge's `Metric` (`sparkplug/payload.go:20`) has no properties at all, so a birth can't carry them.
`spb.Payload_Metric` already has `Properties` (a `PropertySet`), so this is codec and birth work, not protobuf work.

The README's promise is that a host "discovers the whole tag database from the node's birth certificate … as data,
not as configuration you re-enter on the SCADA side". Units and descriptions are the part of that a person would
otherwise retype. The keys hosts look for are the Ignition/Cirrus Link ones: `engUnit`, `documentation`, and
optionally `engLow` / `engHigh` (Mantle for Ignition maps exactly these onto tag properties; its Java simulator is
the only thing exercising that path today). Properties belong in N/DBIRTH only. Template members need them too.

`sparkplug/host/codegen` has comments noting that "the wire's Properties/description does not survive payload
decoding", so the host side's decoder drops them as well; a round trip through this repo's own host would want both
ends.

---

## How the ignition suite fits in

```sh
cd ~/Development/joyautomation/ignition
scripts/dev-up.sh                 # Ignition 8.3 + mosquitto + the module, in Docker
cd integration && go test ./...   # builds nautilus from ../../nautilus on every run
```

It always builds the CLI from this checkout, so it tests whatever is on disk here. For item 1, the test to watch is
`TestASilentNodeIsNoticedWhenItsKeepaliveRunsOutAndRecovers`: it currently **skips** its last step with a message
naming this bug. When the fix is in, that test should pass without the skip, and the guard in
`integration/mantle/mantle_test.go` (search for `KNOWN, AND NOT MANTLE'S`) can be deleted. Item 2 shows up in
`TestBirthCreatesTheTagTree`, which works around it by asserting an integer on a struct member instead of a scalar.

You don't need the Ignition stack to fix or verify item 1. The repro script and a unit test are enough.

---

## Outcome (item 1, 2026-09-19)

### What changed (`sparkplug/`, edge node only)

- **Every MQTT token wait is bounded, and every publish goes through one helper.** `Node.publish` (node.go)
  issues the token and waits on three things: the token's `Done()`, a `tokenTimeout` of 10 s (the same bound
  `sparkplug/host` uses), and a per-connection **lost signal** — a channel paho's connection-lost handler
  closes and replaces. `Connect` uses `WaitTimeout(connectTimeout)`. The `n.inflight.Wait()` in `Stop` is a
  WaitGroup, not a token, and is unchanged.
- **Why the lost signal, and not just `WaitTimeout`.** With a plain 10 s bound the repro still failed: after the
  thaw paho lost *and re-established* the connection inside the same second, so `IsConnectionOpen()` was
  true again while the orphaned token from the old connection was still pending, and the tick sat out the
  full 10 s — past the point where the script counts NDATA. The token has to be abandoned when *its*
  connection goes, which only the lost handler knows.
- **The tick does not publish while paho says the connection is not open** (`IsConnectionOpen()`, checked
  alongside `born`, which lags it). Messages from such a tick go to store-and-forward when it is enabled.
- **seq is assigned at publish time, in wire order, and a message that did not go out hands its number
  back** (`unsentSeq`). The old code assigned seq to live messages at encode time and *then* drained the
  store-and-forward backlog with later numbers but published it first — a seq inversion on every drain. Now
  backlog then live, each numbered as it is sent. A `births` counter keeps a failed publish from handing a
  seq back into a sequence that a birth has restarted meanwhile.
- **Decision for a DATA tick that fails when store-and-forward is on:** the message that failed and everything
  after it in that tick are buffered and replayed as historical after the rebirth; an interrupted drain puts
  its undelivered records back at the front (`storeForward.requeue`). Reason: a QoS 0 token that did not
  complete is not known to have reached the broker, and `store-forward` promises the gap is replayed rather
  than dropped. If the packet did in fact go out the host sees the same sample once live and once
  historical, which a historian tolerates; a hole it cannot recover. Without store-and-forward the sample
  is dropped, as any QoS 0 sample on a dead link would be.
- **An NBIRTH that does not go out leaves the node unborn** (data before a birth is a protocol error); the
  reconnect births again from `onConnect`. DBIRTH/DDEATH failures are logged and skipped.
- `decodeMetric` now carries `IsHistorical` through (the encoder set it; the decoder dropped it — found by
  the new tests).

### Verified

- `sparkplug/silentlink_test.go` (fake `mqtt.Client` whose tokens never complete): five tests, each of which
  **failed on the unfixed code because the tick or birth wedged** (2 s deadline; the real wait was
  unbounded) and pass now in well under a second — the invariant, no-publish-while-not-open, timed-out tick
  buffered and replayed at the right seq, early exit on the lost signal, unborn after a lost NBIRTH.
- `go test -race ./sparkplug/...`: ok (sparkplug 1.3 s, host 13.3 s, codegen 1.0 s). One earlier run of the
  whole tree hit the known `sparkplug/host` mochi-mqtt hang (HANDOFF gotcha); host alone passed on rerun.
- `scripts/repro-sparkplug-silent-link.sh` (via the ignition dev stack's broker): **before** — FAIL, 0 NDATA,
  goroutine parked in `Wait()` at `data.go:59`; **after** — `PASS: 4 NDATA after the reconnect`, log shows
  `connection lost` → `publish not sent … connection lost` → `born`, and the publish goroutine is back in
  `run()`'s select.
- Sparkplug TCK edge-node profile (`NAUTILUS_TCK=1`): 842 pass, 84 n/a, 0 fail.
- Ignition integration suite `TestASilentNodeIsNoticedWhenItsKeepaliveRunsOutAndRecovers` (dev stack in
  Docker, nautilus built from this checkout): reached its last step and **passed without the skip** (51 s;
  the broker gave up on the keepalive 46 s after the freeze). The `KNOWN, AND NOT MANTLE'S` guard in
  `../ignition/integration/mantle_test.go` is removed — the last step now asserts `LevelFt` reaches 56 via
  `expectValue`, like the earlier steps — and the un-guarded test passed on a second run (46 s). Nothing
  else in that repo was touched. (A first run, launched seconds after the gateway restarted and overlapping
  the repro script, failed in Mantle's own first step — Ignition never created the tag tree — and is not
  evidence either way.)

### Left open

- ~~**Store-and-forward does not buffer across a broker outage, only across a primary-host outage.**~~
  Found while making the decision above: `scanAndPublish` returned before the publish pass whenever
  `born` was false, and the connection-lost handler clears `born`, so nothing was buffered from the moment
  paho noticed the loss until the rebirth — whatever the guide promised. **Fixed the same day as a
  follow-up commit on this branch:** unborn with store-and-forward on (and a birth behind it), the tick
  samples in buffer-only mode — no rebirth scheduling, no device events — and the reconnect's birth is
  followed by the replay. Tests: `TestStoreForwardBuffersAcrossABrokerOutage`,
  `TestUnbornNodeWithoutStoreForwardStaysQuiet`.
- The connection-lost handler and `onConnect` are both paho goroutines; paho spawns the reconnect and the
  lost handler back to back, so on a very fast reconnect the handler *could* run after the new session's
  birth and clear `born`. The new `IsConnectionOpen` gate does not cover that ordering. Not observed; noted.

## Outcome (items 2 and 3, 2026-09-19)

Both landed as their own commits on the same branch, after item 1; each can be taken or left.

- **Item 2** (`manifest: an untyped tag's init seeds as the type the program declares it`). Option one from
  the finding: `runtime.expandTags` takes the union of every program's bound globals (the deep set, so a
  library block's `VAR_EXTERNAL` counts) and seeds a plain tag's `init:` through `ir.SeedFromInit` against
  the scalar type declared there. `init: 0` on `Counter : DINT` seeds an integer and births as `Int64`;
  `init: 65` on a REAL is still a Double; `init: 2.5` on an INT is a load error naming the tag and type; a
  tag no program declares still seeds a number as a REAL. No manifest change; the loader's `normalize()`
  is now only the fallback shape. Verified: four new tests in `internal/project`, `go test ./...`, and
  every example's `naut check` + acceptance suite (the sparkplug-host example's `SitesOnline : INT`
  now seeds as an INT and its suite still passes). The tag-model guide gained a paragraph.
- **Item 3** (`sparkplug: births state unit and desc as engUnit/documentation properties`). `Metric` gains
  `Properties`; the codec writes and reads a PropertySet (the decoder used to drop it — the codegen
  comment about that is gone); births attach `engUnit`/`documentation` per tag from the runtime's meta,
  template members under their dotted path; data messages carry none. The host side round-trips them:
  `Binding`/`TagSpec` gain `Unit`, the broker import fills `desc`/`unit` from the birth (a member takes
  its own `engUnit` when stated, else the metric's) into the generated tag file. Verified: codec and
  birth tests over the fake client, a broker-path codegen test, `go test -race ./sparkplug/...`, the
  whole repo, and the TCK edge-node profile (0 fail; pass counts vary run to run: 814/842/828). Not done:
  `engLow`/`engHigh` — the tag meta has no range fields to source them from.
- Nothing was pushed and no PR opened.
