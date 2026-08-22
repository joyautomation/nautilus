---
title: Historian
description: Archive a controller's tags into Postgres and serve downsampled history — one daemon, no toolchain, and the HMI keeps one origin.
---

`nautilus historian` is a separate daemon that polls a controller's tag
API and archives samples into Postgres. Separate on purpose: the runtime
never imports it, so the controller stays pure stdlib, and the archive's
lifecycle (retention, storage, backups) is operated apart from control.

```sh
nautilus historian \
  -source http://plc:8080 \        # or NAUTILUS_SOURCE_URL
  -db postgres://user:pw@db/hist \ # or DATABASE_URL
  -tags 'PIT_*,TempC,LevelPct' \   # glob patterns; default * (all numerics)
  -interval 1s -retention 168h
```

- **What it records:** every numeric or boolean *leaf* matching `-tags`
  and not matching `-exclude` (booleans as 0/1), once per `-interval`,
  from `GET /api/state`. Strings are skipped at any depth.
- **Redundancy-aware for free:** point `-source` at the Service in front
  of the replicas — a standby proxies the poll to the leader, so samples
  keep flowing across a failover with no historian configuration at all.
- **Retention** is a rolling window (`-retention`, default 7 days),
  pruned hourly.

## Struct (UDT) tags

A struct tag's value arrives from `GET /api/state` as a nested JSON
object — an `AnalogInput` tag `FIT_001` with its scaled reading at a
`VALUE` member serializes as `{"VALUE": 42.5, "HH": true, ...}`. The
collector walks into it and records each numeric/bool leaf under a dotted
path: `FIT_001.VALUE`, `FIT_001.HH`. A nested struct member walks further
(`FIT_001.RAW.COUNTS`), and an array member indexes with brackets
(`FIT_001.LOG[0]`, `FIT_001.LOG[1]`, …). String members are skipped at
every depth, same as before.

`-tags` and `-exclude` patterns (`path.Match` glob syntax) are matched
against the **full dotted leaf name** — `*` matches `.` too, since
`path.Match` only treats `/` as a separator. That means:

- A pattern with no dot and no trailing `*` (e.g. `RTU9_FIT_001`) only
  matches a top-level scalar tag of that exact name — it will never match
  a struct's members.
- `*.VALUE` matches the `VALUE` member of every struct tag, regardless of
  prefix or depth.
- `*_FIT_*` matches the whole dotted leaf name, so it picks up both
  `RTU9_WEL15_FIT_001.VALUE` and `RTU9_WEL15_FIT_001.HH` — every leaf
  under a tag whose name contains `_FIT_`, not just one member.

For a UDT-heavy fleet where every instrument is a struct, a good starting
point is:

```sh
-tags '*.VALUE,*.RUNST,*Alm,*.HH,*.H,*.L,*.LL'
```

Use `-exclude` (same glob syntax) to trim noise a broader `-tags` pattern
would otherwise sweep in — e.g. `-exclude '*.SIM*,*.DB'` to drop
simulation/diagnostic members.

## Cutting rows/minute: `-deadband` / `-min-interval`

By default every matched leaf is recorded on every poll — exactly one row
per tag per `-interval`, regardless of whether the value changed. On a
fleet where most instrument state is static between real events, that's a
lot of redundant rows.

Setting `-min-interval` (a duration, default `0` = disabled) turns on
change-based filtering: a tag is recorded when it's new, when it has moved
by more than `-deadband` (an absolute value, default `0`) since it was
last recorded, or when at least `-min-interval` has elapsed since it was
last recorded — a heartbeat, so a flatlined tag still shows up periodically
instead of vanishing from a trend between real changes. Leaving
`-min-interval` at its default `0` preserves the historian's original
behavior (record every matched tag every poll) with no other changes
required.

```sh
-deadband 0 -min-interval 60s
```

is a reasonable starting point for a fleet: record immediately on any
change, or at least once a minute otherwise.

## Serving history to the HMI

The daemon answers:

- `GET /history?from=<ms>&to=<ms>&tags=a,b&maxPoints=600` — downsampled
  `[unixMs, value]` pairs per tag (bucket averages)
- `GET /history/span` — first/last sample and count
- `GET /healthz`

`tags` is a CSV of exact archived leaf names — the same dotted strings
`-tags` matched at collection time, e.g.
`?tags=RTU9_WEL15_FIT_001.VALUE`. There's no globbing at query time, and a
dot isn't special here: samples are stored and queried as plain tag
strings, so a struct member's dotted name works exactly like any other tag.

Point the controller at it and the HMI keeps one origin — the API proxies
`GET /api/history*` through:

```yaml
server:
  historian: http://historian:9100   # or NAUTILUS_HISTORIAN_URL
```

History answers on **any** replica, leader or standby: the archive lives
in Postgres, not the tag store.

## Aggregates

A compliance or daily report needs summary numbers, not raw rows — a
reservoir's peak level, a well's runtime hours, a day's total flow. The
historian computes these server-side, so a report generator never has to
pull a day's worth of samples down just to reduce them itself.

```
GET /history/agg?tags=<CSV>&from=<ms>&to=<ms>&bucket=<duration>&fn=<fn>
GET /history/at?tags=<CSV>&at=<ms>
```

`/history/agg` returns `[{tag, ts, value}, ...]` — one row per tag per
bucket, `ts` the bucket's start (ms epoch, same convention as every other
timestamp this API returns). Omit `bucket` to aggregate the whole
`[from,to)` range as a single row per tag; otherwise `bucket` is a Go
duration (`1h`, `24h`, `15m`) and buckets align to the Unix epoch, not to
`from` — a `24h` bucket lands on UTC midnight, not on whatever time the
query happens to start at.

`fn` is one of:

| `fn` | Meaning |
|---|---|
| `min` / `max` / `avg` / `sum` / `count` | Plain aggregate over the bucket's samples. |
| `first` / `last` | The bucket's earliest/latest sample value. |
| `delta` | `last - first` — a monotonic totalizer's period total (a flow-quantity-index tag), not a sum of raw readings. |
| `ontime` | Seconds a tag was non-zero, read off consecutive samples: for each adjacent pair, the interval up to the next sample counts when the *earlier* sample's value was non-zero. This is the runtime-hours source for a BOOL `RUNST` — divide by 3600 for hours. Precision is bounded by the collector's `-interval`, and the very last sample in a bucket contributes no trailing interval (nothing bounds it); a transition that straddles a bucket boundary is dropped rather than split across the two buckets. Fine for a daily report; not exact to the second. |

`/history/at` answers a snapshot query — each tag's last value
at-or-before `at` — for numbers that are a point-in-time reading rather
than a window aggregate (a reservoir level at 6am, not a min/max/avg of
it). A tag with no sample at-or-before `at` is simply absent from the
result.

**Daily max reservoir level:**

```
GET /history/agg?tags=RES2A.LIT&from=<6am_ms>&to=<next_6am_ms>&fn=max
```

**Well runtime hours from `RUNST`** (a BOOL member, archived as 0/1 by the
collector — see [Struct (UDT) tags](#struct-udt-tags)):

```
GET /history/agg?tags=WEL15.RUNST&from=<6am_ms>&to=<next_6am_ms>&fn=ontime
```

divide the returned `value` by 3600 for hours, matching Ignition's
`UpdateReportRuntimes` rollup (`24 * minutes-running / 1440`).

**Daily flow total from a totalizer**, e.g. a `FIT_*.PREV` flow-quantity-
index tag that only ever increases:

```
GET /history/agg?tags=WEL15.FIT_001.PREV&from=<6am_ms>&to=<next_6am_ms>&fn=delta
```

`delta` (last-first) matches `UpdateReportFlowTotals`'s sum-of-deltas
logic for a monotonic totalizer without needing to fetch and diff raw rows
yourself.

**Reservoir level at a specific time** (e.g. the report's 6am start/end
snapshot, as opposed to a min/max over some window):

```
GET /history/at?tags=RES2A.LIT,RES5B.LIT&at=<6am_ms>
```

**Bucketed series for a chart**, e.g. hourly turbidity peaks over a day:

```
GET /history/agg?tags=PFP.NTU_002&from=<ms>&to=<ms>&bucket=1h&fn=max
```

## From Go

`hist.Sink` is the seam — `Insert(ts time.Time, vals map[string]float64) error`.
`hist.Store` (Postgres) implements it; a different TSDB is a few lines.
The collector half is just HTTP polling, so a custom archiver can also
skip this daemon entirely and sample `Runtime.Tags()` in-process.

A report generator that runs in-process alongside a `hist.Store` (rather
than over HTTP) can call the same aggregation directly:

```go
rows, err := store.Aggregate(ctx, hist.AggQuery{
    Tags:   []string{"WEL15.RUNST"},
    From:   dayStart, To: dayEnd,
    Fn:     "ontime",
})
// rows[i].Value is seconds on; rows[i].Value/3600 is runtime hours.

snap, err := store.Snapshot(ctx, []string{"RES2A.LIT"}, sixAM)
// snap[i].Value is the reservoir level at-or-before sixAM.
```

## Schema

One narrow table, made for the query it serves:

```sql
CREATE TABLE samples (ts timestamptz NOT NULL, tag text NOT NULL, value double precision);
CREATE INDEX samples_tag_ts ON samples (tag, ts DESC);
```

Downsampling happens in SQL (epoch-bucket averages), so a week of
1-second samples ships to a chart as ~600 points.
