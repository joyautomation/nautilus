---
title: Changing I/O while it runs
description: Change a device's address, unit id, register or OPC UA node on a running controller by staging, validating and applying a candidate, then save it as a git commit. An engineer's Configuration page does it with forms.
---

A field device gets a new address, or a register moves. On most systems that
is a download and a restart. Here a changed **address** applies while the
scan runs, and only the changed device's tags read anything but good during
the swap. The change is a **candidate** that goes through visible steps, and
what runs is never in a half-changed state.

## Steps

A candidate is **pending** (staged), then **valid** or **invalid** (checked
against what runs), then **applied** or **failed**. A refused apply changes
nothing. A failed one puts back what it changed.

```sh
# stage a candidate from files you send (or from a commit: {"sha": "..."})
curl -s -X POST localhost:8080/api/io/staged \
  -d '{"driver":"modbus","files":{"modbus_manifest.yaml":"sources: ..."}}'
curl -s -X POST localhost:8080/api/io/staged/1/validate   # what would change, what it would command
curl -s -X POST localhost:8080/api/io/staged/1/apply
```

Validation reads from the device before it lets you apply: a changed device
is probed on a connection of its own (it never writes), and a wrong unit id
or a missing register makes the candidate invalid, with the probe named. A
device that allows only one connection is checked statically instead; say
`validate: static` on its row.

**What may change** is addressing: a Modbus device's `host`, `port` and
`unitid`; a binding's `table`, `address` and scan class; an OPC UA client's
`endpoint` and a binding's `nodeid`; a driver's scan rate; a `devices:` row's
`endpoint` and `unit-id`. Anything else (the tag set, adding or removing a
driver or device, word order, timeouts, scaling, security) is refused by
name, with "restart to apply the rest".

Two safety details. `willCommand` in the validation lists the outputs the
first scan after the apply will command, so you see them before they go. And
`{"revertIfSilent": "10s"}` on the apply puts everything back if a changed
device has not read by then. It is off by default: a reversion on a running
plant can be its own surprise.

An applied change is kept across a restart and a takeover when the project
has `retain:`: a controller that restarts or takes over re-applies it before
its first scan. On a project with `redundancy:` an apply without `retain:` is
refused. Live applies are refused while a restart or takeover is still
re-applying, and during a rolling upgrade of a pair make none until both
controllers run the new build. Under the Kubernetes Lease (`redundancy:`
without `peers:`) a live apply is refused for now, since its replicas
cannot check that the others keep it: deploy the change instead (see the
[HTTP API guide](/guides/http-api/)).

## Forms instead of curl

A signed-in engineer (the `config` permission) sees a **Configuration** page
in the built-in screens: the drivers and devices as forms generated from the
manifest's schema, a diff of what changed, then stage, validate and apply.
`GET /api/config/schema` serves the same schema to your own screens.

## Make it permanent

An applied change lives on the controller until it is in git:

- **`POST /api/config/commit`** commits it to the checkout the controller
  runs from, authored as the person who applied it. A controller built with
  `naut build` has no repository and answers `409`.
- **`GET /api/config/patch`** returns it as a patch: `curl ... | git apply`.
- **`naut pull --io`** writes the I/O files back into your project.
- A change that needs a restart (a device added or removed) is saved with
  **`POST /api/config/save`** and applies at the next restart.

Every step is in the [audit journal](/guides/audit-trail/). The routes,
statuses and the full rules are in the [HTTP API](/guides/http-api/#live-io-configuration),
and the permission is in [Users, roles and sign-in](/guides/auth/).
