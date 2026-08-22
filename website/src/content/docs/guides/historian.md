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

## From Go

`hist.Sink` is the seam — `Insert(ts time.Time, vals map[string]float64) error`.
`hist.Store` (Postgres) implements it; a different TSDB is a few lines.
The collector half is just HTTP polling, so a custom archiver can also
skip this daemon entirely and sample `Runtime.Tags()` in-process.

## Schema

One narrow table, made for the query it serves:

```sql
CREATE TABLE samples (ts timestamptz NOT NULL, tag text NOT NULL, value double precision);
CREATE INDEX samples_tag_ts ON samples (tag, ts DESC);
```

Downsampling happens in SQL (epoch-bucket averages), so a week of
1-second samples ships to a chart as ~600 points.
