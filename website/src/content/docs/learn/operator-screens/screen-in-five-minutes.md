---
title: A screen in five minutes
description: "Draw an operator screen as a JSON file, with no code and nothing to install, and watch a simulated plant move on it."
sidebar:
  order: 1
---

An operator screen shows the people running a plant what they need and
nothing else. In nautilus a screen is a **file**, with no Node, no npm and no
code: you list the equipment, say where it goes, and say which tag each piece
shows. This lesson draws a screen for the simulated plant that `naut new`
makes. About five minutes. You need `naut`
([install](/learn/getting-started/install/)).

## Start with a moving plant

```sh
naut new plant --no-input
cd plant
```

`--no-input` takes the defaults without asking. The project has a simulated heated tank: its
tags include `LevelPct`, `TempC`, `TempSP`, `PumpRun` and `Heater`, and the
simulation moves them, so there is something to look at without writing any
logic.

## Draw the screen

Create `plant.mimic.json` (any name ending `.mimic.json`, anywhere in the
project; a *mimic* is a process diagram):

```json
{
  "name": "Heated tank",
  "canvas": {"width": 900, "height": 520},
  "equipment": [
    {"id": "T1", "component": "Tank", "x": 80, "y": 60, "width": 200, "label": "TK-101", "bind": {"levelPct": "LevelPct"}},
    {"id": "P1", "component": "Pump", "x": 380, "y": 330, "width": 120, "label": "P-101", "bind": {"running": "PumpRun"}},
    {"id": "H1", "component": "Heater", "x": 380, "y": 60, "width": 120, "label": "H-101", "bind": {"powerPct": "Heater"}},
    {"id": "G1", "component": "Gauge", "x": 600, "y": 60, "width": 180, "label": "Temperature", "bind": {"value": "TempC", "setpoint": "TempSP"}}
  ],
  "labels": [
    {"text": "Level", "x": 80, "y": 420, "bind": "LevelPct", "unit": "%", "decimals": 1},
    {"text": "Temperature", "x": 80, "y": 450, "bind": "TempC", "unit": "°C", "decimals": 1}
  ]
}
```

The pieces:

- `canvas` is the size of the drawing; the screen scales it to fit the
  window.
- Each item in `equipment` is a built-in component, placed at `x`, `y` with a
  `width` and a `label`. `bind` connects a part of it to a tag: `"levelPct":
  "LevelPct"` fills the tank to the level the tag says; `"running":
  "PumpRun"` makes the pump look running when the tag is TRUE.
- Each item in `labels` is a live readout: `bind` names the tag, and the
  number appears with its unit and a small trend of the last few minutes.

Check it, then run it:

```sh
naut check
naut run
```

`naut check` should list `screen plant.mimic.json: 4 equipment, 0 pipes` and
end with `0 with errors`: it checks every tag the screen names against the
project, so a misspelt tag is a warning here and not a blank gauge in front
of an operator. If it also warns about text on the *Diagnostics* page, that
page is `naut`'s own; ignore it.

Open <http://localhost:8080>. You see the tank level moving, the heater
glowing as the controller drives it, the temperature gauge with its setpoint
marker, and the readouts with their trends. `naut run` adds a bar with your
screen, a *Diagnostics* page (the controller's scan timing and drivers) and
an *Alarms* page.

## Change it by hand, or by dragging

Edit the JSON and run again, or open the file in VS Code with the nautilus
extension: right-click the file, choose *Open With*, then *Mimic Editor*. It
shows the same screen live, and you move equipment by dragging, bind tags in a
panel, and the file is saved as the same JSON you review in a pull request.

## Next

[A button and a setpoint that write](/learn/operator-screens/a-button-that-writes/):
make the screen control the plant. The [operator screens
guide](/guides/operator-screens/) lists every component and shows pipes.
