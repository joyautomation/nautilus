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

| Component | Props to bind |
|---|---|
| `Tank` | `levelPct` (`shape`: `vessel`, `open` or `pit`) |
| `Pump` | `running`, `speedPct` |
| `Valve` | `openPct` |
| `Heater` | `powerPct` |
| `Gauge` | `value`, `setpoint` |
| `Sparkline` | `values` |
| `CommandButton` | `held`, `disabled`; the tag it writes is the `tag` prop |
| `WriteToggle` | `value`; the tag it writes is the `tag` prop |

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
It is an ordinary Svelte 5 component: its props are what `props` and
`bind` set, plus these, which every piece of equipment is given:

- `width`, `height` and `label`, from the document;
- `look`, the screen's look;
- `write(name, value)`, which writes one tag on the controller and resolves
  to `null` once it landed, or to the reason it didn't.

So a filter cell can carry its own Backwash button, placed with
`"props": { "request": "F2_BackwashReq" }` and its state bound:

```svelte
<script lang="ts">
	let { state = 1, label = '', request = '', write }: {
		state?: number;
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
	<button type="button" onclick={backwash} disabled={state !== 1}>Backwash</button>
	{#if error}<small>{error}</small>{/if}
</div>
```

Import the kit's helpers and drawings from `@joyautomation/nautilus-hmi`.
A `{Name}.component.json` beside the component holds the sample props its
preview shows and its pipe ports. Open the `.svelte` and press the preview
button in the editor title to see it render as you type.
