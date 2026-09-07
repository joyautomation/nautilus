---
title: Program history
description: The running controller serves its own commit history. Review every change as a diff, and warm-swap back to any revision.
---

A traditional PLC keeps no record of where its logic came from. A nautilus
controller does: `GET /api/program/history` serves every commit that touched
the project (author, date, subject, and the full diff) from the controller
itself. This is the mirror of the commit-to-running-controller pipeline:
running controller back to commits.

```bash
curl localhost:8080/api/program/history
```

```json
{
  "built": "d1d29dc3…",          // HEAD when this artifact was built
  "editable": true,
  "commits": [
    { "sha": "d1d29dc3…", "author": "…", "date": "…",
      "subject": "the settle test stays and watches…",
      "diff": "diff --git a/program.fbd b/program.fbd\n…",
      "activatable": true },
    …
  ],
  "programs": [                   // the running layer, per task
    { "task": "main", "pou": "Main", "hash": "1aa388c81e2d", "dirty": false },
    …
  ]
}
```

`built` is the commit the deployed baseline corresponds to; the `programs`
list is the live layer on top of it, so one read shows both what CI shipped
and whether online edits have drifted from it — the drift a traditional PLC
makes invisible. A build from an uncommitted tree is flagged
(`builtDirty: true`) rather than passed off as clean lineage.

## Where the history comes from

The history is captured **where git exists** and travels with the artifact:

- **`nautilus build`** snapshots the project's git history into the emitted
  binary (you'll see `— N commits of program history embedded` on the build
  line). A deployed controller, even a distroless container on an air-gapped
  plant network with no git binary, no `.git` dir, and no route out, serves
  history from the data it carries.
- **`nautilus run`** in a checkout captures live from the repo on first
  request.

Either way the endpoint never fails for lack of provenance: outside a repo
it serves an empty history. Snapshots are stored git-style — files dedupe on
git's own blob ids — so fifty commits of a project cost kilobytes, not
megabytes (`unzip -p <binary> .history` shows exactly what shipped).

## Activating a revision

Any captured commit can be **run**:

```bash
curl -X POST localhost:8080/api/program/activate -d '{"sha":"d1dfc2b"}'
```

Activation rebuilds every task's program source as it was at that commit —
libraries composed ahead, exactly as boot composes them — and warm-swaps
each one. It is an [online edit](/guides/online-edits/) in every way that
matters:

- **Gated the same** — `online-edits: true` plus the write token if one is
  set.
- **State survives** — retained variables (PID integrals, timers, counters)
  migrate by name and type; what can't carry over is reported per task in
  `resets`.
- **All or nothing** — every source compiles before any swap happens, so a
  controller is never left running half of two revisions. A commit that no
  longer compiles is rejected whole.
- **Undoable** — each program keeps its one-step rollback, and activating
  `built` returns to exactly what CI shipped.
- **Ephemeral** — like any online edit, a restart boots the binary's
  embedded programs. Making a revert permanent means deploying it.

After an activation the history reports `active: <sha>`; a hand edit or
rollback clears the claim, since the running source then matches no one
commit.

## What activation cannot do

Time travel covers **logic only**. The cold plane (tasks, tags, drivers,
retain and redundancy wiring) is left as the running manifest booted it. A commit that changed topology (added a task, renamed one) is refused
with a `409` naming the difference: deploy that commit's image instead.
That's the same two-plane rule online edits follow — logic changes live,
infrastructure ships through [CD](/guides/deployment/).
