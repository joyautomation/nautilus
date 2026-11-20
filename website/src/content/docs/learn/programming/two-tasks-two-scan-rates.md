---
title: Two tasks, two scan rates
description: "Run a fast program and a slow program in one controller, each on its own scan, and see the difference exactly in a test."
sidebar:
  order: 3
---

Not everything needs to run every 100 ms. A fast loop wants a short scan; a
report that checks a level once a second does not. A **task** is a program
together with how often it scans, and one controller runs several. About ten
minutes.

## Declare two tasks

In `nautilus.yaml`, each task names a program file and a scan period:

```yaml
name: tasks

tasks:
  - program: fast.st
    scan: 100ms
  - program: slow.st
    scan: 1s

tags:
  - { name: FastScans, role: output, type: DINT, init: 0, desc: Runs of the fast task }
  - { name: SlowScans, role: output, type: DINT, init: 0, desc: Runs of the slow task }

driver:
  type: memory
```

Each program counts its own runs:

```st
PROGRAM Fast
FastScans := FastScans + 1;
END_PROGRAM
```

```st
PROGRAM Slow
SlowScans := SlowScans + 1;
END_PROGRAM
```

(`slow.st` is the same with `SlowScans`.) Both programs see the same tags, so
they can hand each other values; they differ only in when they run.

## Test it in virtual time

```yaml
tests:
  - name: ten seconds is 100 fast scans and 10 slow ones
    steps:
      - advance: 10s
        expect: { FastScans: 100, SlowScans: 10 }
```

```sh
naut test -v
```

```text
tasks_test.yaml
  ok    ten seconds is 100 fast scans and 10 slow ones 100 scans, 10s

1 tests, 1 passed, 0 failed
```

Ten seconds of virtual time ran exactly 100 scans of the fast task and 10 of
the slow one. The test says what a scan rate *is*, and it will fail if
someone changes `1s` to `500ms` without meaning to.

## Watch it run

```sh
naut run
curl -s localhost:8080/api/state | jq -c .tags
```

```text
{"FastScans":34,"SlowScans":3}
```

After a little over three seconds the fast task has run about 34 times and the
slow one 3. The `scan` object in the same response reports the main task's
timing: its target period, the last, average and maximum scan time, and the
jitter.

## Which task owns the field I/O

The first task in the list owns the field: a driver's inputs are written
before its scan and its outputs sent after. Put the program that needs the
fastest I/O first. A task can also be given a priority for real-time work; that is a
later topic ([Real-time](/guides/real-time/)).

## Next

The [function blocks and tasks guide](/guides/blocks-and-tasks/#more-than-one-program-tasks)
covers tasks in full, including how programs share tags: all they share is
tags, and a tag should have one owner.
