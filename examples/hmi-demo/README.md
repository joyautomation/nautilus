# hmi-demo — a SvelteKit operator screen on the nautilus HMI kit

A runnable operator screen built entirely from `@joyautomation/nautilus-hmi`.
It reads one SSE stream from a running controller and renders the whole kit:
the tank faceplate, alarm pills, live trends, setpoint controls, the
**field-driver status cards**, and the scan diagnostics.

```sh
npm install
npm run dev            # → http://localhost:5173, proxying /api to localhost:8080
```

Point it at any running nautilus controller:

```sh
# a no-Go manifest controller (faceplates + trends + scan diagnostics)
( cd ../heated-tank-nogo && nautilus run )

# …or one with real field drivers, to see the EtherNet/IP + Sparkplug cards
CONTROLLER_URL=http://localhost:8081 npm run dev
```

## What it demonstrates

- **`DriverStatusPanel`** — the EtherNet/IP poller and Sparkplug B publisher
  as live connection cards (state, uptime, poll/message counters, the
  Sparkplug device's birth state). Against a loopback controller it shows the
  honest empty state; against `examples/client60` it shows both links live.
- **`Tank` / `Gauge` / `Trend` / `StatusPill`** — the process faceplates,
  bound to `frame.tags`. Gauges carry a setpoint marker; alarm pills flip on
  the interlock tags.
- **`NumberField` + write-back** — setpoint edits POST to `/api/tags`. The dev
  server proxies `/api` to the controller so the browser sees one origin and
  the write isn't same-origin-gated.
- **`ScanDiagnostics`** — the PLC scan loop, fed straight from `frame.scan`.
- **`RealtimeClient`** — one generic SSE client drives everything; `theme`
  and `ThemeSwitch` flip the whole screen light/dark.
- **`/legacy` — the legacy-port components**, the pieces you reach for when
  the screen already exists in Ignition, FactoryTalk or WinCC and the job is
  to reproduce it: `CoordinateCanvas` (a fixed, scaled coordinate plane),
  `EquipSymbol` (a raster symbol with SCADA state chrome), `ScaleBar`,
  `StatusRow`, `LevelTank`, the four schematic glyphs (`TankGlyph`,
  `PumpGlyph`, `ValveGlyph`, `FlowLink`) and the write-back trio
  (`WriteNumber`, `WriteToggle`, `CommandButton`). This page needs no
  controller — every value is one slow sine wave, so the bands cross, the
  flow link starts marching and the status rows change colour on their own.

## How it's wired

`vite.config.ts` proxies `/api` → `CONTROLLER_URL` (default
`http://localhost:8080`), so both the SSE reads and the tag writes are
same-origin. The app is a pure client SPA (`ssr = false`,
`adapter-static` with an `index.html` fallback) — `npm run build` produces a
static bundle you can drop beside a controller or behind any reverse proxy
that routes `/api` to the runtime.

The single page is `src/routes/+page.svelte` — a compact, real example of
consuming the kit; copy from it into your own screens.

## The process mimic

The P&ID graphic is **data, not markup**: `heated-tank.mimic.json` places
equipment (kit components + tag bindings), routes pipe runs (points +
flow bindings), and labels — `<Mimic doc={...} tags={frame.tags}>`
renders it live and scales it to its container. Screens, routing, and
logic stay ordinary SvelteKit; the mimic is the part of an HMI that's
genuinely spatial, so it's the part that gets a document format (and,
next, a graphical VS Code editor). Pass your own components via the
`registry` prop to place custom equipment.

## Custom components + connection points

Equipment on the mimic isn't limited to the kit's built-ins. `src/lib/HeatExchanger.svelte`
is an app-authored component (shell-and-tube symbol, four stubs: tube-side
in/out left/right, shell-side in/out top/bottom) that lives here in the demo,
not in `@joyautomation/nautilus-hmi` — the point being that any Svelte
component can join a mimic:

1. Write the component (theme vars, a `width` prop, a couple of
   live-bindable props — see `HeatExchanger.svelte`'s `active`/`label`).
2. Pass it to `<Mimic>` via the `registry` prop: `registry={{ HeatExchanger }}`
   (see `+page.svelte`).
3. Reference it from a `.mimic.json` like any other equipment:
   `{ "component": "HeatExchanger", ... }` — `heated-tank.mimic.json` places
   one (`E101`) on the line from the tank to the zone-13 demand, with a
   second coolant loop through its shell-side stubs.

The VS Code mimic editor doesn't know your custom component's markup, so it
draws it as a dashed placeholder chip — but it can still route pipes to it,
because connection points ("ports") are declared separately in a sidecar
file next to the component: a file named `{ComponentName}.component.json`
ANYWHERE in the workspace declares metadata for that component. Today the
only key is `ports`, as `[x, y]` fractions of the rendered box (the shape is
deliberately general — a per-component metadata file, not a ports-only one
— so it's the natural growth point for things like default props or
tag-bind hints later). Here that's
[`src/lib/HeatExchanger.component.json`](./src/lib/HeatExchanger.component.json),
next to `HeatExchanger.svelte`:

```json
{ "ports": [[0.5, 0], [0, 0.5], [1, 0.5], [0.5, 1]] }
```

Component names are already unique in the registry, so the file is
name-keyed, not path-keyed — it can live anywhere the project finds
convenient (next to the component's source is the natural place, and where
the editor creates one automatically the first time you set ports on a
component it can't find a `.svelte` file for). The same convention overrides
a **built-in** kit component's ports too — a `Tank.component.json` anywhere
in a project takes precedence over the registry default.

Open a `{ComponentName}.component.json` directly for a small dedicated
Component Editor (the component centered with its ports as draggable dots),
or open a `.mimic.json`, select a placeholder chip, and press `p` to edit
its ports in place — either way the edit lands in the sidecar and every
instance of that component, in every mimic in the project, picks it up.

## Pipe anchors + port exit directions

A port can carry an explicit `dir` (`left`/`right`/`up`/`down` — the
direction a pipe leaves it); left absent, it's inferred from the port's
edge position. A pipe end can name a port instead of a raw point
(`"from"`/`"to": { "equip": "P101", "port": "out" }`) — that end's
position is then DERIVED from the port every render, so it can't go
stale, and moving the equipment moves the pipe with it. In
`heated-tank.mimic.json`: the city-feed pipe's end is anchored to
`P101`'s `in` port, one coolant pipe end is anchored to `E101`'s
`shellIn`, and `P101`'s discharge pipe is anchored to its `out` port —
which sits on the pump's right edge, so it infers `dir: "right"` and the
pipe exits **horizontally** before turning up into the tank, in both the
mimic editor and this shipped page.

One wrinkle for a **custom** component like `E101`/`HeatExchanger`: its
ports live in the `HeatExchanger.component.json` sidecar, but sidecars
are a project/editor-time convenience (`mimicComponentIndex.ts`
aggregates every one it finds in the workspace) — they never ship with
the built app, so the runtime `<Mimic>` can't resolve an anchor against
one. `E101`'s equipment entry in the doc carries an explicit `ports`
override instead (the SAME values as the sidecar), which — being part
of the document itself — resolves identically whether you're in the
editor or looking at this page live. A built-in (Tank, Pump, Valve,
Gauge, Sparkline) doesn't need this: its default ports are compiled
into `@joyautomation/nautilus-hmi` itself, so a sidecar-free instance
(like `P101` here) already anchors correctly at runtime.

## Faceplates

Clicking equipment on the mimic opens its **faceplate** — the kit's
`Faceplate` popup chrome (title/path, ×/Esc/backdrop close) + `Tabs`,
with the content composed from ordinary components: a MONITORING panel
(live rows + `StatusPill`s), a CONTROL panel (`NumberField` setpoints,
command buttons), and tabbed subviews (a `Trend`, tuning fields). See
the `faceplate === 'T101'` block in `+page.svelte` — faceplates are
plant-specific by nature, so the kit ships the chrome and you compose
the content.

Note on writes in dev: the vite proxy rewrites the `Origin` header to
the controller's own origin (see `vite.config.ts`) — without that, the
controller's same-origin write guard correctly 403s browser writes that
arrive with the dev server's origin.
