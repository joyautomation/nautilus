---
title: Keep history, and ask it questions
description: "Run the historian beside a controller, watch it archive tags into Postgres, read a window of history back, and ask for a peak, a run time and a value at a moment."
sidebar:
  order: 4
---

A controller knows what the tank is doing now. A plant also needs what it did
at 6 a.m., the peak since yesterday and how long a pump ran. That is the
**historian**: a second program that watches the controller and writes what it
sees into a database. In this lesson you start one, look at what it keeps, and
ask it questions. About fifteen minutes. You need `naut`
([install](/learn/getting-started/install/)), and **Docker**, for a throwaway
Postgres database (the one thing the historian needs that `naut` does not
include). The commands also use `curl`, and `jq` and GNU `date` (on macOS, use
`gdate` from coreutils) to tidy and time-stamp the answers. You work in three
terminal windows: one for the controller, one for the historian, one to ask
questions. Give it about two minutes of running before step 5, so there is
history to ask for.

## 1. A database to keep it in

```sh
docker run -d --name lesson-pg -e POSTGRES_PASSWORD=lesson \
  -p 127.0.0.1:55432:5432 postgres:16
```

This starts Postgres, reachable only from your computer, on port 55432. The
password is for a lesson and a database you delete at the end; a real one gets
a real password. Give it a few seconds to start.

## 2. A controller with something moving

```sh
naut new plant --no-input
cd plant
naut run
```

This is the simulated heated tank from [A screen in five
minutes](/learn/operator-screens/screen-in-five-minutes/): its `LevelPct`,
`TempC` and `PumpRun` move with no hardware. Leave it running. If port 8080 is
already in use, `naut run` says so and prints the address it moved to; use that
one wherever this lesson says `8080`.

## 3. Start the historian

The historian is a separate program, so it gets its own terminal. In a second
terminal, in the `plant` folder:

```sh
DATABASE_URL='postgres://postgres:lesson@127.0.0.1:55432/postgres?sslmode=disable' \
  naut historian -source http://127.0.0.1:8080 -addr 127.0.0.1:9100 \
  -tags 'LevelPct,TempC,PumpRun' -interval 1s
```

```text
naut historian: connected to database, polling http://127.0.0.1:8080
naut historian: serving history on 127.0.0.1:9100
```

Once a second it asks the controller for its tags and records the three named
in `-tags` (a comma-separated list of patterns; the default is every number).
True/false tags such as `PumpRun` are kept as 1 and 0. Leave it running.

## 4. Look at what it keeps

In a third terminal, ask the database directly (these use the container you
started; the numbers will differ):

```sh
docker exec lesson-pg psql -U postgres -c \
  "select count(*), count(distinct tag) as tags, min(ts)::time(0), max(ts)::time(0) from samples"
```

```text
 count | tags |   min    |   max    
-------+------+----------+----------
   270 |    3 | 06:04:44 | 06:06:23
(1 row)
```

One narrow table, `samples`: a time, a tag name and a value. About three rows a
second here, one per tag. Everything below is a way of asking this table a
question without writing SQL. The historian answers on port 9100:

```sh
curl -s localhost:9100/history/span
```

```text
{"count":270,"first":1795241084409,"last":1795241183409}
```

(The times are milliseconds since 1970, the way every time in the API is given.)

## 5. Ask the controller for history

An operator screen talks to the controller, not to the historian, and the
controller can pass history questions along so there is still one address. Stop
`naut run` (Ctrl-C in its terminal). In `nautilus.yaml`, add one line under
`server:`:

```yaml
server:
  addr: "localhost:8080"
  historian: http://127.0.0.1:9100
```

Run `naut run` again (if it moved to another port before, it moves again; keep
using that one). Now ask for the last five minutes of the temperature,
split into at most six points (`date` fills in "now" in milliseconds):

```sh
NOW=$(date +%s%3N)
curl -s "localhost:8080/api/history?from=$((NOW-300000))&to=$NOW&tags=TempC&maxPoints=6" | jq -c .series
```

```text
{"TempC":[[1795241050000,59.94],[1795241100000,59.97],[1795241150000,60.39]]}
```

(`jq` only tidies the output; the real values have many more decimals than
shown here, and you will see as many points as your minutes of history allow.)
Each pair is a time and the *average* over that slice, which is how a week of one-second samples fits a chart in about six
hundred points. Ask for a shorter window or a larger `maxPoints` and the slices
get finer.

Notice what happened to the history while you restarted the controller: nothing
was lost, and nothing was invented either. The historian kept running, and for
the few seconds the controller was down there are no samples, so a chart shows
a gap rather than a straight line drawn across it. You can see the gaps (one for each time you restarted it, so your list may show
one, not two):

```sh
docker exec lesson-pg psql -U postgres -c \
  "select ts::time(0) as gap_starts, (next_ts - ts) as gap from (select ts, lead(ts) over (order by ts) next_ts from samples where tag='TempC') s where next_ts - ts > interval '3 seconds'"
```

```text
 gap_starts |       gap       
------------+-----------------
 06:05:19   | 00:00:04.467435
 06:05:40   | 00:00:04.824729
```

## 6. A peak, a run time, a value at a moment

A report wants single numbers. The historian computes them, so you never fetch
a day of rows to reduce them yourself. The highest level and temperature in the
window (the `ts` of each answer is the start of the window you asked about, not
the moment of the peak):

```sh
curl -s "localhost:8080/api/history/agg?tags=LevelPct,TempC&from=$((NOW-300000))&to=$NOW&fn=max" | jq -c .
```

```text
[{"tag":"TempC","ts":1795240883665,"value":61.1},{"tag":"LevelPct","ts":1795240883665,"value":74.9}]
```

How many seconds the pump ran (`ontime` counts the time a tag was non-zero;
divide by 3600 for hours):

```sh
curl -s "localhost:8080/api/history/agg?tags=PumpRun&from=$((NOW-300000))&to=$NOW&fn=ontime" | jq -c .
```

```text
[{"tag":"PumpRun","ts":1795240883665,"value":43.8}]
```

And the tank's level and temperature as they were half a minute ago:

```sh
curl -s "localhost:8080/api/history/at?tags=LevelPct,TempC&at=$((NOW-30000))" | jq -c .
```

```text
[{"tag":"LevelPct","ts":1795241130409,"value":45.4},{"tag":"TempC","ts":1795241130409,"value":59.9}]
```

Other functions: `min`, `avg`, `sum`, `count`, `first`, `last` and `delta`
(the change over the window, for a totalizer that only counts up). Add
`&bucket=1h` to get one number per hour instead of one for the whole window.
Asking `at` a time before the first sample (say, with the historian only twenty
seconds old) returns an empty list: the historian does not guess.

## 7. Clean up

Stop the historian and `naut run` with Ctrl-C, and delete the database:

```sh
docker rm -f lesson-pg
```

## Before a real plant

- **A real database.** Use a Postgres you back up, with a proper password and
  `sslmode` as your policy says. The historian takes it from the `DATABASE_URL`
  environment variable, never from a file you commit.
- **How long to keep it.** The default keeps seven days and prunes hourly
  (`-retention 720h` for thirty). To keep fewer rows, `-min-interval 60s` records
  a tag when it changes and at least once a minute otherwise.
- **Who may read it.** The historian answers requests for `localhost` or an IP
  address, and refuses a name it was not told about. If the controller reaches it
  by name, that name goes in its `NAUTILUS_ALLOWED_HOSTS`, and a controller with
  a token needs the same token on the historian (`NAUTILUS_TOKEN`).
- **A trend on a screen.** The [Historian guide](/guides/historian/) covers
  these and the history-backed trend an HMI app binds with `useTrend`.

## Next

[Alarms](/learn/operator-screens/alarms/) is the other thing a plant expects
from its screens: what is wrong now, and who has seen it.
