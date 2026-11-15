---
title: Operator screens
description: A *.mimic.json is an operator screen as data — equipment placed on a canvas, pipes between it, tags bound to it — that the VS Code extension edits graphically and runs live, with buttons that command the plant.
---

An operator screen in nautilus is a file: `*.mimic.json`, anywhere in the
project. It places equipment on a canvas, runs pipes between it, and binds
each piece to the tags it shows. The VS Code extension opens it in a
graphical editor, and with a controller running (`naut run`, or the
playground) the screen animates with the process and its controls write to
the controller. The JSON stays the file you commit, review and diff, the
same as a program.

The same document renders in a SvelteKit HMI through the
[`@joyautomation/nautilus-hmi`](https://www.npmjs.com/package/@joyautomation/nautilus-hmi)
`<Mimic>` component, for the spatial part of a larger app.

## The document

```json
{
  "name": "Filter gallery",
  "canvas": { "width": 1200, "height": 700 },
  "equipment": [
    { "id": "T1", "component": "Tank", "x": 80, "y": 120, "width": 160, "label": "TK-101",
      "bind": { "levelPct": "TK101_Level" } },
    { "id": "P1", "component": "Pump", "x": 300, "y": 360, "label": "P-101",
      "bind": { "running": "P101_Running" } },
    { "id": "Start", "component": "CommandButton", "x": 300, "y": 460,
      "props": { "tag": "P101_StartPB", "label": "Start" } }
  ],
  "pipes": [
    { "id": "suction", "from": { "equip": "T1", "port": "bottom" }, "to": { "equip": "P1", "port": "in" },
      "points": [], "routing": "orthogonal", "bind": { "flowing": "P101_Running" } }
  ],
  "labels": [
    { "text": "TK-101", "x": 80, "y": 300, "bind": "TK101_Level", "unit": "%", "decimals": 1 }
  ]
}
```

- **`canvas`** is the logical size; the screen scales it to fit.
- **`equipment`**: each item names a `component` and its top-left `x`, `y`
  in canvas units, with an optional `width` and `height`, a `label`, static
  `props`, and `bind`, which maps a component prop to a tag name. A leading
  `!` negates a BOOL tag (`"stopped": "!P101_Running"`). `behind: true`
  draws it under the pipes (a wet well with pumps inside it).
- **`pipes`**: a polyline of `points`, or anchored at either end to an
  equipment's named port (`from`, `to`); `routing: "orthogonal"` draws
  right angles. Bind `flowing` (and `rate`) to animate the flow.
- **`labels`**: static text, or with `bind` a live readout.
- **`look`**: `isa101` (the default: flat grey, colour only for a state or
  an abnormal condition) or `realistic`.

## Built-in equipment

| Component | Props to bind | Ports |
|---|---|---|
| `Tank` | `levelPct` (`shape`: `vessel`, `open` or `pit`) | `top`, `left`, `right`, `bottom` |
| `Pump` | `running`, `speedPct` | `in`, `out` |
| `Valve` | `openPct` | `in`, `out` |
| `Heater` | `powerPct` | |
| `Gauge` | `value`, `setpoint` | |
| `Sparkline` | `values` | |
| `CommandButton` | `held`, `disabled`, `disabledReason`; the tag it writes is the `tag` prop | |
| `WriteToggle` | `value`; the tag it writes is the `tag` prop | |

## Pipes and ports

A pipe is a polyline in canvas units, the same units as `x` and `y`:

```json
{ "id": "header", "points": [[300, 820], [1500, 820]],
  "bind": { "flowing": "P501_Running" }, "color": "#3e6f96" }
```

Anchor an end to a piece of equipment instead with `from` or `to`,
`{ "equip": "<id>", "port": "<port name>" }`. The end then follows the
equipment wherever it moves, and `points` holds only the bends in between
(it may be empty: `[]`). `routing: "orthogonal"` turns each pair of points
into a right-angled L; without it the segments are straight.

- **`flowing`** (a BOOL, or a number above zero) animates the flow in the
  direction the points run, so draw a pipe from where the water comes from.
- **`rate`** sets how fast, as a fraction of full flow, 0 to 1. Bind it to
  a tag that holds that fraction (a flow over its design flow, computed in
  the program), not to the flow in engineering units.
- **`color`** is the pipe's flow colour; the wall stays the screen's grey.

A port is a named point on an equipment's box, as fractions of it: `x: 0,
y: 0.5` is the middle of the left edge. The built-ins have the ports in the
table above. A component of your own has none until its
`{Name}.component.json` gives it some (below).

## Controls that write

A screen is for operating the plant, not only watching it. `CommandButton`
is a momentary pushbutton: pressed, it writes `true` to its `tag`, then
`false` 400 ms later (`pulseMs`; `0` latches `true` for a command bit the
program clears itself). `WriteToggle` is a lamp and a switch for a BOOL
tag: it shows `value` and writes the opposite when pressed.

```json
{ "id": "BW2", "component": "CommandButton", "x": 40, "y": 600,
  "props": { "tag": "F2_BackwashReq", "label": "Backwash F-2", "pulseMs": 0 },
  "bind": { "held": "F2_BackwashReq" } }
```

The write is the same one Toggle Bit and **Set Live Value…** make: a POST
to the controller's [tag API](/guides/http-api/). It goes while live values are
on and a controller is running; otherwise, and when the controller refuses
it (a forced tag, say), the control shows why beside itself. In the editor,
a click on a control operates it while a controller is connected; with
live values off, the whole piece is the editor's to select and drag again.

## Your own components

Any `*.svelte` component in the project can be placed by name, next to the
built-ins: the editor finds it, compiles it and renders it on the canvas.
This is how a screen gets what the built-ins don't draw: a filter cell in
section, a valve with its limit switches, a whole gallery.

It is an ordinary Svelte 5 component: its props are what `props` and
`bind` set, plus these, which every piece of equipment is given:

- `width`, `height` and `label`, from the document. The equipment's box is
  that size, so a component that draws to `width: 100%; height: 100%` (an
  `<svg>` with a `viewBox`, say) fills it and scales when it is resized;
- `look`, the screen's look;
- `write(name, value)`, which writes one tag on the controller and resolves
  to `null` once it landed, or to the reason it didn't.

So a filter cell can carry its own Backwash button, placed with
`"props": { "request": "F2_BackwashReq" }` and `"bind": { "filterState":
"F2_State" }`:

```svelte
<script lang="ts">
	let { filterState = 1, label = '', request = '', write }: {
		filterState?: number;
		label?: string;
		/** The tag a press writes, set in the document's `props`. */
		request?: string;
		write?: (name: string, value: unknown) => Promise<string | null>;
	} = $props();
	let error = $state('');
	async function backwash() {
		error = (await write?.(request, true)) ?? '';
	}
</script>

<div class="cell">
	<b>{label}</b>
	<button type="button" onclick={backwash} disabled={filterState !== 1}>Backwash</button>
	{#if error}<small>{error}</small>{/if}
</div>
```

Don't name a prop or variable `state` in a component that uses
`$state(...)`: Svelte then reads `$state` as a store subscription to it,
and the component throws when it mounts. `naut hmi build` stops on it.

Import the kit's helpers and drawings from `@joyautomation/nautilus-hmi`.

A `{Name}.component.json` beside the component gives it pipe ports and the
sample props its preview renders with:

```json
{
  "ports": [
    { "name": "influent", "x": 0.5, "y": 0 },
    { "name": "effluent", "x": 0.25, "y": 1 },
    { "name": "air", "x": 0, "y": 0.85 }
  ],
  "preview": { "label": "F-2", "level": 5.8, "filterState": 1 }
}
```

Each port's `x` and `y` are fractions of the box. With `"part": "<name>"`
they are fractions of an element the component marks
`data-part="<name>"` instead, so the port stays on that part at any size.
Open the `.svelte` and press the preview button in the editor title to see
it render with the preview props as you type; there, you can also drag the
ports into place.

## Units: what repeats, written once

Four filters, each with six valves and their pipes, is the same drawing
four times. Write it once in the mimic's `units`, and naut draws it once
per value of `each`:

```json
"units": [
  {
    "id": "filter",
    "each": [1, 2, 3, 4],
    "step": [420, 0],
    "equipment": [
      { "id": "F{n}", "component": "FilterCell", "x": 230, "y": 370,
        "props": { "label": "F-{n}" }, "bind": { "level": "F{n}_Level" } },
      { "id": "F{n}_INF", "component": "IsaValve", "x": 335, "y": 300,
        "bind": { "zso": "F{n}_INF_ZSO", "zsc": "F{n}_INF_ZSC" } }
    ],
    "pipes": [
      { "id": "F{n}_inf", "points": [[355, 270]], "to": { "equip": "F{n}_INF", "port": "a" } }
    ],
    "labels": [{ "text": "F-{n}", "x": 240, "y": 680 }]
  }
]
```

- `{n}` in any string (an id, a tag in `bind`, a label, the equipment a
  pipe ends on) becomes each value in turn. A string that is exactly
  `"{n}"` becomes the value itself, so `"props": { "n": "{n}" }` passes the
  number. `"as": "p"` names a different placeholder, `{p}`.
- The first copy sits where it's written; each next one moves by `step`
  (`[dx, dy]`, canvas units).
- A copy is equipment, pipes and labels like any other: `naut check`
  checks its tags and ports, ids must be unique (put `{n}` in them), and
  a pipe outside the unit can end on a copy (`"equip": "F3_EFF"`).
- The editor shows the copies outlined and locked; change them in the
  unit, in the file.

## A P&ID, step by step

1. **Lay out the process** with equipment: built-ins where they fit, your
   own components where they don't (a filter cell, say), and what repeats
   per unit in `units`, written once.
2. **Give each component its ports** in its `.component.json`, where the
   plant's pipes meet it.
3. **Run the pipes** between ports, anchored at both ends, in the
   direction the process flows, and bind `flowing` to what proves the flow
   (a pump running, a valve's open limit switch).
4. **Add the readouts** as `labels` with `bind`, beside what they measure,
   with the instrument's tag number in `text`.
5. **Put the controls on the drawing** next to what they command, with
   `disabled` (and `disabledReason`) bound so a command the program would
   ignore can't be pressed.
6. **Run it**: `naut run`, then open <http://localhost:8080>: the screen,
   live, for the operator. In VS Code, open the `*.mimic.json` with live
   values on to operate it while you edit.

## Serving the screens

`naut run` builds the project's screens itself (no Node, no npm) and
serves them at `/`, with the engineering dashboard moved to `/_nautilus/`:

- **With no `server.hmi`**, a project with `*.mimic.json` files gets a page
  for each: the first at `/`, the others by file name
  (`screens/pumps.mimic.json` is `/pumps`), under a bar with the project's
  name, the pages, the connection and a theme switch. The pages are live
  and their controls write, as in the editor.
- **For more than mimics**, write the screen as an app and point
  `server.hmi` at its folder: `src/routes/+page.svelte` is `/`,
  `src/routes/trends/+page.svelte` is `/trends`, a `+layout.svelte` wraps
  the pages under it, `+page.js` and `+layout.js` `load` functions run in
  the browser, components go in `src/lib` (imported as `$lib/...`), and
  `static/` is served as it is. That is SvelteKit's layout, client-side:
  the same folder also builds with SvelteKit and npm. Go is the only
  backend, so there are no `+server.js` or `+page.server.js` files: a page
  reads the controller through the [tag API](/guides/http-api/).

A screen imports Svelte, the HMI kit, the 3D kit
(`@joyautomation/nautilus-hmi-3d`), three and Threlte (`@threlte/core`,
`@threlte/extras`), which `naut` carries, and the project's own files.

`naut check` checks every mimic against the project (each tag a binding,
a label or a control names is declared, each pipe ends on a port its
equipment has) and builds the screens; `naut hmi build` builds them alone,
the same way, without running them. Both list what is wrong, each with its
file and line: a component that names
a prop `state` and also uses `$state(...)` (Svelte then reads `$state` as
a store, and the component throws when it mounts), an import `naut`
doesn't carry, a component a mimic names that isn't in the project.
`naut build` builds them into the controller it makes, so the controller
serves them as they are.

With Chrome, Chromium or Edge on the machine, `naut check` then opens each
page of the built screen at desktop and phone size and reports what a
person would see wrong:

- a page that throws, or comes up blank (an error);
- a pipe that draws nothing, naming the end that isn't there (an error);
- a shape drawn far outside its own drawing, usually a coordinate passed
  as text (`y="130"`, so `y + 8` is `"1308"`) (a warning);
- a page that scrolls sideways on a 390 px phone (a warning).

The screen is served from naut's own server over the project's runtime
without scanning it: no driver starts, no write reaches anything, and
every tag shows its initial value. `NAUT_BROWSER` names the browser to
use (`NAUT_BROWSER=none`, or `naut check -render=false`, skips it).
Without a browser, `naut check` says the screen wasn't rendered and
carries on.
