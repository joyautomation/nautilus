---
title: Online edits
description: Change logic while it runs — warm swaps with retained state, diff against the controller, and pull field edits back to git.
---

nautilus has two planes. The **cold plane** — connections, the tag manifest,
scan classes, server wiring — is Go, and changes ship through CI/CD as a new
binary. The **hot plane** — the IEC program and tag values — can change live,
the way you online-edit a traditional PLC. Because the program is data on a
VM, a warm swap carries retained state (PID integrals, timers, counters)
across by name and type; a failed compile leaves the running program
untouched, so a typo can never fault the controller.

Enable it per controller (off by default — pushing logic is code execution
on a control system):

```go
srv := server.New(rt, server.Options{OnlineEdits: true})
```

Then from VS Code: **Download Program to Controller** warm-swaps your open
program, **Diff Program with Controller** shows running-vs-workspace, and a
status-bar indicator flags when the controller runs something other than the
committed file. Edits are ephemeral by design — a restart reverts to the
program the binary embeds, so **committing the program to git is the only way
an edit becomes permanent**. The rule of thumb falls out of the two planes:
logic you want to tune online, write in IEC; infrastructure, write in Go.

## Pulling field edits back

Pulling a field edit back to git closes the loop. **Pull Program from
Controller** (VS Code) or `naut pull --host <controller>` writes the
running program back into your program file — the inverse of download — so
you review it with `git diff` and commit. Only the program file is rewritten;
generated type files are never touched. `naut pull --check` reports drift
and exits non-zero, so CI can fail a build when a controller has un-pulled
edits. Composition is a single definition shared by the runtime, the language
server, download, and pull, so a program round-trips losslessly.

## Working against a remote controller

All of this — live values, online edits, pull — works over the network, not
just against a local process. A scaffolded controller binds loopback by
default; set `NAUTILUS_ADDR=0.0.0.0:8080` to expose the tag API to other
machines, and point the editor at it with the `nautilus.runtimeUrl` setting
(`naut pull` takes `--host`). Exposing the API on the network also
exposes its write surface, so set `NAUTILUS_TOKEN` on the controller and the
matching `nautilus.token` in the editor — reads and `naut pull` stay
open, but tag writes and online edits then require the token.

## Every program, both directions

Programs are addressed by POU name — `PROGRAM <Name>` is a program's
identity. `GET /api/program` lists the resource's programs; a `PUT` routes
automatically by the POU name in the submitted source, and `?pou=` / `?task=`
select one explicitly for GET/rollback. In VS Code that means a workspace
with one program file per task Just Works: open the file, Download/Diff/
Rollback target that task's program, retained state carries across the swap.
And `naut pull` reconciles the whole resource: every controller program
pulls back into the workspace file declaring its POU (a new program lands in
`<POU>.st`/`.fbd`), so a field edit to any task is reviewable and
committable — `--check` fails CI on drift in any of them.

## Editing against history

The controller also carries its own git provenance: every commit that
touched the project, reviewable as diffs and warm-swappable as a whole —
"run the logic from three commits ago" is one request, with the same gates,
state migration, and rollback as any online edit. See
[Program history](/guides/program-history/).
