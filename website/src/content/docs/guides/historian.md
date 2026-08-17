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

- **What it records:** every numeric tag matching `-tags` (booleans as
  0/1), once per `-interval`, from `GET /api/state`. Strings and compound
  values are skipped.
- **Redundancy-aware for free:** point `-source` at the Service in front
  of the replicas — a standby proxies the poll to the leader, so samples
  keep flowing across a failover with no historian configuration at all.
- **Retention** is a rolling window (`-retention`, default 7 days),
  pruned hourly.

## Serving history to the HMI

The daemon answers:

- `GET /history?from=<ms>&to=<ms>&tags=a,b&maxPoints=600` — downsampled
  `[unixMs, value]` pairs per tag (bucket averages)
- `GET /history/span` — first/last sample and count
- `GET /healthz`

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
