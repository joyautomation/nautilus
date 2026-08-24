---
title: Streaming and data quality
description: How an HMI subscribes to a controller — delta frames, tag filters, and the per-tag quality that stops a screen from guessing whether a number is real.
---

Two things separate a demo HMI from one a plant runs on: how much of the
tag store it has to pull to draw a screen, and whether it can tell a live
number from an old one. This guide covers both, because they are the same
conversation — what the controller says on every tick, and what it means.

The numbers this page is built on come from a real 10,000-tag controller:
`GET /api/state` was **571 KB**, and a single SSE client pulled about
**2 MB every ten seconds** — four complete renderings of the plant per
second, whether or not anything moved. That is fine for the one wall screen
it was built for. It does not survive a shift's worth of tablets, and there
is no version of "add more clients" that gets better.

## Delta frames

Ask for them with `?delta=1`:

```
GET /api/stream?delta=1
```

The first frame is a complete snapshot. Every frame after it carries only
the tags whose value **changed** since the previous frame *sent to that
client*:

```jsonc
// first frame
{"ts":1770000000000,"seq":1,"full":true,"tags":{ /* all 10,000 */ },"scan":{…}}
// every frame after
{"ts":1770000000250,"seq":2,"tags":{"RTU9_WEL15_LIT_001":4.31},"scan":{…}}
```

Two fields make it a protocol rather than a trick:

- **`seq`** — this client's frame counter, from 1, reset on every new
  connection.
- **`full`** — *replace* your state, don't merge into it. True on the first
  frame, on each periodic resync (30 s by default), and whenever the
  controller's set of tag names changed, which is the one difference a
  delta cannot express: there is no way to say "this tag is gone".

Everything else on the frame — the scan diagnostics, driver status, alarm
counts, `quality` — is sent **whole every time**. They are a few kilobytes
against a store of hundreds, and a client that had to reassemble them would
gain nothing.

### What it costs

Measured on 10,000 plant-shaped tag names, with the scan diagnostics
included in both (`go test ./server/ -run XXX -bench FrameBytes`):

| Churn per tick | Full frame | Delta frame | Ratio |
| --- | --- | --- | --- |
| 1 % | 284 KB | 5.5 KB | **51× smaller** |
| 5 % | 284 KB | 16.8 KB | **17× smaller** |
| 20 % | 284 KB | 59 KB | **4.8× smaller** |

Five percent is a realistic steady state for a plant that is running rather
than starting up. The floor a delta frame cannot go below is the
diagnostics block (~3 KB), which is why the 1 % row is 51× and not 280×.

The server side gets *cheaper*, not more expensive, because the per-tick
work is shared: one sweep of the store for all delta clients, one rendering
of each changed value, and one JSON encoding per distinct
(generation, filter) pair — and clients tick together, so that is usually
one encoding for the whole fleet. At fifty connected clients a delta
broadcast costs about a tenth of what a full broadcast does.

### Why it cannot lose an update

This is the part worth trusting, because a merge that quietly goes wrong
does not throw or blank a screen — it leaves one number frozen at whatever
it last was, on a page that otherwise looks alive.

The server tracks, per client, the store generation that client has been
brought up to date at (see [write generations](/guides/tag-model/#performance-notes-write-generations)
— the whole delta mechanism is one `uint64` per client, and "what changed
since" is one integer comparison per tag with no value comparison
anywhere). That generation advances **only when a frame is actually
enqueued**. The broadcast loop drops frames for a client too slow to keep
up — it must, or one tablet on bad wifi stalls the loop — and a dropped
frame simply leaves the generation where it was, so the next frame carries
what the dropped one would have.

A dropped frame therefore costs **latency, never content**. Which means a
skipped `seq` cannot mean "a frame went missing" — it can only mean the
transport mangled the stream. The client's response is to reconnect, which
yields a fresh full frame by construction. It never negotiates a repair
with a transport that has already proved unreliable.

The periodic resync is not a crutch for that argument; it is the bound on
how long a client can stay wrong if something *outside* the argument ever
does go wrong. Tune or disable it with `server.Options{ResyncInterval: …}`
(negative disables).

### Deltas are opt-in

A client that asks for nothing gets exactly the stream this endpoint has
always served: whole frames, no `seq`, no `full`. The frame shape is a
public API with clients this repo does not own, and switching them to
partial frames would corrupt every one of them in a way that looks like the
plant went quiet rather than like a protocol change.

`?full=1` forces whole frames even alongside `?delta=1` — the escape hatch
for a client with reason to distrust its own merge.

## Tag filters

A screen that draws forty points should pull forty points:

```
GET /api/stream?tags=RTU9_*,*_LIT_*
GET /api/state?tags=RTU9_*,*_LIT_*
```

Comma-separated glob patterns, matched with Go's `path.Match` against the
whole dotted tag name. The separator `path.Match` splits on is `/`, which
no nautilus tag name contains, so a `*` spans a whole name: `Tank*` matches
both `Tank101` and `Tank101.Level`, and `*.Level` matches the member
address form a screen binds with.

The filter applies to every frame including the first, and to the `quality`
map alongside the tags — a filtered stream never carries quality for a tag
it is not sending. It also applies to `/api/state`, so a screen's initial
load matches its subscription instead of pulling the whole plant to read
forty points.

The two reductions compose, and each is useful alone: filters help most
when a screen is small, deltas when the plant is quiet. Diagnostics, driver
status and alarm counts are never filtered — a subscription narrows tags,
not the controller's own health.

A pattern `path.Match` cannot compile is a `400`, not a silent
match-nothing; a screen bound to a typo should show an error, not an empty
plant.

## Per-tag quality

A tag store holds values. It does not, on its own, hold any statement about
whether a value is worth believing — and an HMI that has to guess will
guess with a heuristic like "is this number −9999?" or "has it moved
lately?", which goes wrong in both directions. A legitimately −9999-scaled
analog reads as dead; a genuinely dead node whose last reading was
plausible reads as live.

Frames now carry a `quality` map:

```jsonc
{
  "ts": 1770000000000,
  "tags": {"RTU9_WEL15_LIT_001": 4.31, "TempSP": 65},
  "quality": {"RTU9_WEL15_LIT_001": "stale", "RTU4_FIT_002": "notConnected"}
}
```

Four values, because an operator screen makes exactly four distinctions:

| Value | Meaning | What a screen should do |
| --- | --- | --- |
| `good` | current value from a healthy source | show it |
| `stale` | was good, no longer refreshed — this is its last reading | show it, greyed, with its age |
| `bad` | the source calls this reading untrustworthy | show the fault, not the number |
| `notConnected` | the source has never delivered | show "no data" — there may be no value at all |

Two rules keep it affordable on a 10,000-tag controller:

- **Only non-`good` entries appear.** An absent name is `good`. A healthy
  plant omits the field entirely, so quality costs a working controller
  zero bytes.
- **A name in `quality` need not be in `tags`.** `notConnected` is exactly
  the source that has never delivered a value, so there is nothing to
  publish under it — and the binding shows the reason rather than an
  unexplained blank.

### Where it comes from

Two sources, in this order.

**The driver**, when it implements `io.QualityReporter`:

```go
// Optional refinement on io.Driver. Return only the non-Good entries.
type QualityReporter interface {
	Driver
	Quality() map[string]Quality
}
```

It is the only thing that actually knows. A Sparkplug host driver knows
which edge node died and which has never birthed; an EtherNet/IP driver
knows which connection dropped. Whatever it says about a tag stands.
Implementing it is never required — a driver that reports nothing behaves
exactly as it did before quality existed. `io.Memory` implements it
(`SetQuality`) so a simulation or a test can exercise the whole path
without a field bus.

**The runtime's own derivation**, for tags the driver said nothing about:
when the last input *read* failed — or has not happened yet — every
driver-bound input is `stale`. The scan ran on last-known values, which is
what `stale` means, and it is a fact the runtime holds whether or not the
driver reports anything. A plain `io.Memory` or a hand-written driver gets
honest staleness for free.

A failed output **write** deliberately does not do this. An actuator
refusing a command says nothing about how old the readings are, and marking
every input stale over it would put a bad-quality badge on every screen in
the plant every time one valve sulked.

### The capability flag

`GET /api/meta` reports `"quality": true` when this controller can report
quality at all. Check it before rendering a quality indicator, because the
false case is **invisible**: an empty `quality` map looks exactly like a
healthy plant, and a screen that cannot tell the two apart paints a
confident green badge on a controller that has no idea. `/api/meta` also
reports `"deltas": true` for `?delta=1` and `?tags=` support.

## From the HMI kit

`RealtimeClient` speaks all of this, and **opts into deltas by default** —
the point being that nothing downstream notices:

```ts
const rt = new RealtimeClient<NautilusFrame>({
  tags: ['RTU9_*'],   // subscribe to a subset (optional)
  delta: true         // the default
});
rt.start();

// frame.tags is always COMPLETE — the client merges deltas for you
rt.frame?.tags.RTU9_WEL15_LIT_001;

// and now a screen can stop guessing
rt.isGood('RTU9_WEL15_LIT_001');            // false when stale/bad/absent-source
rt.quality('RTU9_WEL15_LIT_001.CTL1HSP');   // 'stale' — resolves to the root tag
```

`quality()` resolves a dotted member path to its root tag: quality belongs
to the *delivery*, and a UDT arrives from its source whole, so
`P101.Drive.Speed` is exactly as trustworthy as `P101` is. An exact entry
for the full path still wins, for a controller that reports at member
granularity.

Against a controller that predates any of this, the client falls back to
plain pass-through automatically — frames arrive with no `seq`, and it
publishes them untouched. Asking for deltas is never a compatibility risk.

The merge rules live in one small, dependency-free module
(`hmi/src/lib/delta.ts`) with their own tests, precisely because they are
the piece of the kit that can silently mislead an operator.
