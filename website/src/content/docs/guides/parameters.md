---
title: Parameter sets
description: Export a controller's tuned setpoints to a file, compare them with the manifest or another set, apply a set all at once or not at all, and let the controller commit what operators tuned back to git.
---

A **parameter** is a scalar setpoint tag with a literal `init:` in the
manifest. The repository keeps the `init:`; operators tune the live value.
Over time the two drift. This page is the workflow that keeps them in step,
and each command below only reads, except `import`.

```yaml
retain: {}
tags:
  - { name: StartLevel, role: setpoint, init: 65.0, unit: "%", min: 10, max: 90 }
```

Only numeric and boolean setpoints count, and only retained ones: with no
`retain:` section a setpoint returns to its `init:` on every restart, so
there is nothing to drift.

## See the drift

```sh
naut params diff --host plc1       # live values that differ from the manifest's init
```

Each parameter is `in-step`, **plant-ahead** (an operator edit the
repository does not have), **repo-ahead** (a redeploy brought a new `init:`
and the plant still holds the value the old one gave) or **conflict** (both
moved, to different values). `--host` may be a URL for a
controller with `server.tls`. For a controller with a token, set
`NAUTILUS_TOKEN`.

## Save a set, compare it, apply it

```sh
naut params export --host plc1 -o set.yaml          # the live values as a file
naut params export --host plc1 --drift -o drift.yaml  # only what moved
naut params diff   --host plc1 --file set.yaml --check
naut params import --host plc1 --file set.yaml --dry-run
naut params import --host plc1 --file set.yaml
```

The file is `version: 1` and a `parameters:` map of tag name to value. `diff
--file` shows what applying the set would change and marks a line `REFUSED`
for a name the controller does not know, a value of another kind, or one
outside the tag's `min` and `max`. `--check` exits 1 on a difference or a
refusal and 2 when the controller cannot be reached.

`import` is **all or nothing**: one refusal (a value outside its limits, a
forced tag, a name that is no parameter) stops the whole set and nothing is
written. It needs the `config` permission and, for every tag in the set,
the permission you would need to write it alone. The first refusal names
the tags. `--dry-run` asks the controller what it would do and writes
nothing. On a pair a standby forwards the request to the leader.

## Write tuned values back to git

```yaml
parameters:
  writeBack: commit   # off (default) | patch | commit
```

With `patch`, `GET /api/parameters/patch` returns the plant-ahead values as
a patch for `git apply`. With `commit`, a controller running from a git
checkout commits them itself, one commit per editor, authored as that
editor, 30 seconds after the last edit. A value that was imported lands as
the person who imported it. A `repo-ahead` or `conflict` value is never
written: a person decides. The endpoints, statuses and refusals are in the
[HTTP API](/guides/http-api/#exporting-and-comparing-a-parameter-set).

See also [Deployment](/guides/deployment/), which moves a site's
configuration, and [Users, roles and sign-in](/guides/auth/) for the
`config` permission.
