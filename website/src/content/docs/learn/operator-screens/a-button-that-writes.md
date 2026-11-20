---
title: A button and a setpoint that write
description: "Add a push button and a setpoint control to an operator screen, and check from the API that they really change the plant's tags."
sidebar:
  order: 2
---

A screen is for operating the plant, not only watching it. In this lesson
you add two controls to the [screen from the last
lesson](/learn/operator-screens/screen-in-five-minutes/): a button that
acknowledges the horn, and a field that changes the temperature setpoint.
About five minutes.

## Add the controls

Add these two items to `equipment` in `plant.mimic.json` (the full file is
below):

```json
{"id": "Ack", "component": "CommandButton", "x": 380, "y": 450, "props": {"tag": "HornAck", "label": "Acknowledge horn", "kind": "neutral"}},
{"id": "SP", "component": "SetpointGroup", "x": 600, "y": 260, "width": 260, "props": {"title": "Setpoints", "items": [{"tag": "TempSP", "label": "Temperature", "unit": "°C", "decimals": 1}]}}
```

- **`CommandButton`** is a momentary push button, like a real one. Pressed,
  it writes `true` to its `tag`, then `false` 400 ms later (`pulseMs`).
  `kind` colours it: `start` green, `stop` red, `neutral` plain.
- **`SetpointGroup`** is a small form of fields, one per item in `items`:
  type a number and press Enter to write it. It reads the tags too, so it
  always shows the current value. Give it a `width`, never a height.

The whole screen:

```json
{
  "name": "Heated tank",
  "canvas": {"width": 900, "height": 520},
  "equipment": [
    {"id": "T1", "component": "Tank", "x": 80, "y": 60, "width": 200, "label": "TK-101", "bind": {"levelPct": "LevelPct"}},
    {"id": "P1", "component": "Pump", "x": 380, "y": 330, "width": 120, "label": "P-101", "bind": {"running": "PumpRun"}},
    {"id": "H1", "component": "Heater", "x": 380, "y": 60, "width": 120, "label": "H-101", "bind": {"powerPct": "Heater"}},
    {"id": "G1", "component": "Gauge", "x": 600, "y": 60, "width": 180, "label": "Temperature", "bind": {"value": "TempC", "setpoint": "TempSP"}},
    {"id": "Ack", "component": "CommandButton", "x": 380, "y": 450, "props": {"tag": "HornAck", "label": "Acknowledge horn", "kind": "neutral"}},
    {"id": "SP", "component": "SetpointGroup", "x": 600, "y": 260, "width": 260, "props": {"title": "Setpoints", "items": [{"tag": "TempSP", "label": "Temperature", "unit": "°C", "decimals": 1}]}}
  ],
  "labels": [
    {"text": "Level", "x": 80, "y": 420, "bind": "LevelPct", "unit": "%", "decimals": 1},
    {"text": "Temperature", "x": 80, "y": 450, "bind": "TempC", "unit": "°C", "decimals": 1}
  ]
}
```

Run `naut check`, then `naut run` and open <http://localhost:8080>.

## Try them, and check

Press **Acknowledge horn**: it writes `HornAck` TRUE for 400 ms, too short to
see on the dashboard. Type `72` in the Temperature field and press Enter, then
read the tag back:

```sh
curl -s localhost:8080/api/state | jq .tags.TempSP
```

```text
72
```

The screen and the HTTP API are the same controller, so what an operator does
on the screen is what a script would do with `POST /api/tags`. Setpoint
tags are the right target for a control: they belong to the operator.

## When a control does nothing

- A control writes a tag the program or driver owns, such as an `input`, and
  the controller refuses it: the control shows why beside itself.
- The tag is **forced** (see [forcing](/guides/forcing/)): the write is
  refused until the force is removed.

Anyone who can reach the controller's address can press these buttons, so a
controller on a network needs a token and users: see [Users, roles and
sign-in](/guides/auth/).

## Next

The [operator screens guide](/guides/operator-screens/) has panels that open
on a click, pipes between equipment, your own components and units that
repeat.
