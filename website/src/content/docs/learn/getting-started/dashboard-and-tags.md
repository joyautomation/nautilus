---
title: Read the dashboard and the tags
description: "What each tag role means, how to read live values from the dashboard and the HTTP API, and how to force an input while you test."
sidebar:
  order: 4
---

Every value a controller has is a **tag**, and every tag has a **role** that
says who writes it. This lesson uses the `hello` project from the last
lesson (`naut run` in its folder) to see the four roles at work.

| Role | Who writes it | In `hello` |
| --- | --- | --- |
| `input` | the field driver, before every scan | `Sensor` |
| `setpoint` | an operator (a person or a screen); exists from the first scan | `Setpoint` |
| `output` | the program, after every scan, for the driver to send | `Actuator` |
| `state` | the program, for itself | none yet |

A tag's role is a promise about ownership. The controller refuses a write to
an `input` because the driver would overwrite it within a scan, and a write
to a program's `output` would be overwritten by the program.

## Read the tags

The dashboard at <http://localhost:8080> shows them. The same values are
available to other programs over HTTP:

```sh
curl -s localhost:8080/api/state | jq -c .tags
```

```text
{"Actuator":50,"Sensor":0,"Setpoint":50}
```

(`jq` only tidies the output; plain `curl` works.) Change a setpoint the
same way:

```sh
curl -s -X POST localhost:8080/api/tags -d '{"name":"Setpoint","value":70}'
```

A successful write answers `204 No Content` and prints nothing.
`Actuator` is now `70`.

## Drive an input with a force

`Sensor` has no field behind it here, so nothing moves it. Writing to it is
refused, and the reply says what to do instead. A **force** holds a tag at a
value, against its driver, until you remove it:

```sh
curl -s -X POST localhost:8080/api/forces -d '{"name":"Sensor","value":20}'
curl -s localhost:8080/api/state | jq -c .tags
```

```text
{"Actuator":50,"Sensor":20,"Setpoint":70}
```

`Actuator` is `Setpoint − Sensor` = `50`. Remove the force and the sensor
goes back to what the driver says:

```sh
curl -s -X DELETE localhost:8080/api/forces/Sensor
```

Forces are for bring-up and testing; they are not kept across a restart.
[Forcing](/guides/forcing/) covers the rest, including forcing from VS Code.

## Where to go next

- [Your first program](/guides/first-program/) writes your own logic and
  tests for it, and is the next step of this course.
- [The tag model](/guides/tag-model/) is the full story of roles and how
  tags come to exist.
- [The HTTP API](/guides/http-api/) lists every endpoint.
