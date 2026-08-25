# @joyautomation/nautilus-hmi

The **HMI / digital-twin layer of [nautilus](https://github.com/joyautomation/nautilus)** — a
reusable Svelte 5 component library for building operator screens on top of any nautilus runtime.

It ships three things:

1. **SCADA faceplates** — `Tank`, `Gauge`, `Trend`, `Pump`, `Valve`, `Pipe`, `StatusPill`,
   `Sparkline`, `Histogram`, `NumberField`, plus `ThemeSwitch` / `MotionSwitch`. All are pure
   SVG/CSS, animate smoothly under streaming data, and are driven entirely by CSS custom
   properties (`var(--…)`) so they re-skin from the token layer.
2. **A generic realtime client** (`RealtimeClient` / `createRealtimeClient`) — one SSE stream in,
   commands out over POST. It is agnostic to your frame shape: it hands you the latest parsed JSON
   frame (typed by a generic parameter) and tracks connection **freshness** rather than trusting
   `EventSource` events (which lie across a proxy failover). A `TrendBuffer` helper keeps a rolling,
   windowed history you can bind straight into `Trend`.
3. **A themeable token layer** (`theme.css`) — light & dark `[data-theme]` tokens (surfaces, ink,
   grid/axis, a validated categorical chart palette, status colors, interaction tokens) plus a
   reduced-motion rule and optional base element styles.

It is the HMI layer of nautilus: SvelteKit + SSE, token-themed, and it works with any nautilus
runtime's `/api/stream` endpoint (and the conventional `/api/command` for writes).

## Install

```sh
npm install @joyautomation/nautilus-hmi svelte
```

Svelte 5 is a peer dependency. This kit assumes a SvelteKit (or Vite + `@sveltejs/vite-plugin-svelte`)
host so `.svelte` files are compiled by the consumer.

## Usage

Import the token layer once (e.g. in your root `+layout.svelte` or `app.css`):

```ts
import '@joyautomation/nautilus-hmi/theme.css';
```

Wire the realtime client to your runtime's SSE stream and render faceplates:

```svelte
<script lang="ts">
	import { onMount } from 'svelte';
	import { Tank, Gauge, Trend, createRealtimeClient, TrendBuffer } from '@joyautomation/nautilus-hmi';

	// Describe just the fields your screen reads — the client is generic.
	type Frame = { ts: number; level: number; tempC: number; heaterPct: number };

	const level = new TrendBuffer(180); // keep 3 minutes

	const rt = createRealtimeClient<Frame>({
		url: '/api/stream',
		onFrame: (f) => level.push(f.ts, f.level)
	});

	onMount(() => {
		rt.start();
		return () => rt.stop();
	});
</script>

{#if rt.frame}
	<Tank levelPct={rt.frame.level} tempC={rt.frame.tempC} heaterPct={rt.frame.heaterPct} label="T-101" />
	<Gauge value={rt.frame.tempC} min={0} max={100} unit="°C" label="Temp" />
	<Trend series={[{ name: 'Level', color: 'var(--s1)', points: level.points }]} unit="%" />
{/if}

<span>{rt.connected ? 'live' : 'stale'}</span>
```

Send an operator command back to the runtime:

```ts
await rt.send('setSetpoint', { value: 42 }); // POST /api/command  { cmd, ...fields }
```

Write a tag — a whole tag, or one **member** of a UDT tag by dotted path.
`writeTag` resolves to `null` on success, or to the controller's reason when it
refuses (a misspelled member, a type mismatch, a driver-owned input, a missing
token), so a faceplate can show that on the control instead of pretending the
command landed:

```ts
const err = await rt.writeTag('P101.START', true); // POST /api/tags
await rt.writeTag('P101', { LVL: { CTL1HSP: 60 } }); // several members at once (a merge)
await rt.writeTag('TempSP', 65, { token }); // cross-origin writer
```

## Subscriptions: deltas and data quality

Against a nautilus controller, `RealtimeClient` **asks for delta frames by
default**. After the first full snapshot the controller sends only the tags that
changed, and the client merges them — `rt.frame.tags` is always complete, so
nothing downstream notices. On a 10,000-tag controller at 5% churn that is
~17 kB a tick instead of ~280 kB, per client. Pass `delta: false` for whole
frames; a controller that predates deltas is detected and passed through
untouched, so asking is never a compatibility risk.

Narrow the subscription with globs — a screen that draws forty points should
pull forty points:

```ts
const rt = new RealtimeClient<NautilusFrame>({
	tags: ['RTU9_*', '*_LIT_*'], // matched against whole dotted tag names
	delta: true // the default
});
```

And a screen can stop guessing whether a number is real. The controller reports
per-tag **quality** — the axis the kit could not see before, since `tags.ts`
detects a tag's *absence* but a published-yet-stale value looked exactly like a
live one:

```ts
rt.isGood('RTU9_WEL15_LIT_001'); // false when stale / bad / never-connected
rt.quality('AI_001.LVL.CTL1HSP'); // 'good' | 'stale' | 'bad' | 'notConnected'
```

`quality()` resolves a dotted member path to its root tag — a UDT arrives from
its source whole, so a member is exactly as trustworthy as its tag. **Check
`/api/meta`'s `quality` flag before drawing a quality badge**: an empty quality
map looks exactly like a healthy plant, and on a controller that cannot report
quality a confident green badge is a lie.

`rt.seq` counts frames on the current connection and `rt.resyncs` counts gap
recoveries — normally zero for the life of a session. See the
[Streaming and data quality](https://nautilus.joyautomation.com/guides/streaming/)
guide for the protocol.

## Alarms

A nautilus controller built with an `alarm/` engine (definitions computed from BOOL tags, ISA-18.2
state) serves it at `GET /api/alarms*` and rides a counts-only `alarms` summary on every stream
frame. `createAlarmClient` wires the two together; `AlarmBanner` / `AlarmTable` / `AlarmJournal`
render what it exposes. **Same house rule as everything else in the kit: components take props and
emit callbacks, and never call fetch themselves** — only `alarms.ts`'s client does. Types and JSON
shapes are contracted against `docs/design/alarms.md` (see `src/lib/alarms.ts`).

```svelte
<script lang="ts">
	import { onMount } from 'svelte';
	import {
		createRealtimeClient,
		createAlarmClient,
		AlarmBanner,
		AlarmTable,
		toast,
		type NautilusFrame
	} from '@joyautomation/nautilus-hmi';

	const rt = createRealtimeClient<NautilusFrame>();
	const alarms = createAlarmClient(rt, { url: '/api/alarms' });

	onMount(() => {
		rt.start();
		alarms.start(); // seed the list; frame.alarms.rev drives refetches from here
		return () => {
			rt.stop();
			alarms.stop();
		};
	});
</script>

<AlarmBanner summary={alarms.summary} now={Date.now()} onclick={() => goto('/alarms')} />

{#if alarms.supported}
	<AlarmTable
		alarms={alarms.instances}
		operator="rchon"
		onack={(ids, by) => alarms.ack(ids[0] === '*' ? 'all' : ids, by).then(() => toast.success('acked'))}
		onshelve={(id, until, by) => alarms.shelve(id, (until - Date.now()) / 1000, by)}
		onunshelve={(id, by) => alarms.unshelve(id, by)}
		onselect={(a) => goto(`/faceplate/${a.id}`)}
	/>
{:else}
	<p>This controller has no alarm engine configured.</p>
{/if}
```

`AlarmClient` (returned by `createAlarmClient`) exposes:

- `instances` — the full active + unack-RTN + shelved list from the last successful fetch;
  `active` / `shelved` are derived filters over it.
- `summary` — updated from *every* frame that carries `frame.alarms` (cheap: it's counts only); the
  full list is only refetched from `GET /api/alarms` when `frame.alarms.rev` changes, per
  `shouldRefetch` (exported, unit-testable in isolation from any network or component).
- `supported` — flips to `false` if `GET /api/alarms` 404s, i.e. this controller was built with no
  alarm definitions. Starts `true` until the first response lands.
- `ack(ids | 'all', by)`, `shelve(id, seconds, by)`, `unshelve(id, by)`, `journal(from, to, filters)`.

`AlarmTable`'s columns, widths, and default sort (Active Time, descending) come verbatim from the
Perspective view the brief was reverse-engineered from: Priority | Active Time | State | Label |
Pipeline | Ack Time | Ack User. State colors follow the reserved status tokens
(`--crit`/`--serious`/`--warn`/`--ink-2`/`--muted`) and are never color-alone — every state and
priority also carries a label and a glyph, same convention as `ConnectionBadge`'s `STATE_META`.
2,000+ row lists are handled with simple pagination rather than a virtualization dependency.
`AlarmJournal` takes a `from`/`to` (epoch ms) range plus an `onrange` callback so the host re-fetches
via `alarms.journal(...)` and hands back fresh `events`; its CSV export is a client-side
`Blob`/`URL.createObjectURL` download — this is an ordinary web app, not a sandboxed artifact, so
`<a download>` works fine here.

## Porting a legacy HMI

The components above assume you are drawing a new screen. Porting an existing Ignition,
FactoryTalk or WinCC project is a different job, and these five cover the parts of it that are the
same everywhere.

**`CoordinateCanvas`** — a fixed plane of absolutely-placed symbols, scaled to the viewport. Every
legacy screen ever drawn is one of these, and it has to keep its aspect ratio: a pipe must stay
attached to the pump it feeds, so the schematic never reflows, it only grows and shrinks. Give it a
spec (`{ width, height, items }`, see `./canvas.ts`) and a **registry** mapping each node's `t` to
one of your components — the kit owns the geometry, you own what the nodes mean:

```svelte
<script lang="ts">
	import { CoordinateCanvas, type CanvasNode } from '@joyautomation/nautilus-hmi';
	import Tank from './Tank.svelte';
	import Pump from './Pump.svelte';
</script>

<CoordinateCanvas
	{spec}
	registry={{ tank: Tank, pump: Pump }}
	graphics={['pipe', 'label', 'image']}
	visible={(n) => !n.p?.visibleTag || isSet(n.p.visibleTag as string)}
	href={(n) => (n.p?.href as string) || undefined}
>
	{#snippet leaf(node: CanvasNode)}<Unmapped {node} />{/snippet}
</CoordinateCanvas>
```

`registry` is checked first, `leaf` renders whatever it does not cover, `containers` (default
`['coord', 'flex']`) recurse into `node.c`, and `graphics` names the types that FILL their box
instead of centring a component in it. `visible` / `style` / `href` are where live bindings land.

**`EquipSymbol`** — a raster symbol with SCADA state chrome: the run wash, the simulate/comm-fail
outline, the fault bell and the A/M · R/L chips. Legacy symbol libraries are PNGs with wildly
different aspect ratios, so pass **both** `width` and `height` and the picture is `contain`-fit
into that box, which is what those packages do.

**`ScaleBar`** — the moving analog indicator: a value against a scale cut into coloured alarm
bands. A DISABLED limit is `null`, not a separate enable flag, so a UDT's per-limit enable bit maps
straight onto the prop: `h={HENB ? HSP : null}`.

**`StatusRow`** — one device per line: mode chips, a name, a value, on a background that carries
the state. `state` is `'on' | 'off' | 'fault' | 'unknown'`, and `unknown` never collapses into
`off` — an absent point and a stopped pump mean opposite things.

**`LevelTank`** — a level-only vessel with band marks, a percentage and a volume. (`Tank` is the
heated-vessel twin: `tempC`, `heaterPct`, an animated coil. Both exist because they answer
different questions.)

For a network view you lay out yourself, the four glyph primitives — `TankGlyph`, `PumpGlyph`,
`ValveGlyph`, `FlowLink` — render bare SVG `<g>` elements to drop inside your own `<svg>`.
`FlowLink` draws its marching dashes **only** when `flowing` is true, so a still line means still
water; drive it from a meter with a deadband, never from "the pump is commanded on".

## Writing back

`RealtimeClient.writeTag(name, value)` resolves to `null` on success or the controller's refusal
reason. Three controls take exactly that function, so they work against any transport:

```svelte
<WriteNumber tag="TempSP" label="Setpoint" value={frame.tags.TempSP} units="°C" write={rt.writeTag} />
<WriteToggle tag="Enable" label="Enable" value={frame.tags.Enable} write={rt.writeTag} />
<CommandButton tag="Start" label="Start" kind="start" pulseMs={400} write={rt.writeTag} />
```

Each renders the refusal on the control rather than swallowing it, and each takes `readonly` +
`readonlyReason` (or `disabled` + `disabledReason`) for a point the runtime will not accept — a
faceplate that silently fails to command is worse than one that says it cannot.

## Faceplates, cards and confirmation

Three pieces that go together: one faceplate layout for every equipment family, one card for what a
schematic becomes on a narrow screen, and one confirm step in front of anything irreversible.

### The faceplate standard — `FaceplateShell`

A plant has four or five equipment families and no more. It does **not** have one faceplate per
device — that is how a project ends up with six hand-written valve popups that drift apart. So the
layout is fixed by the shell and only the content varies: header (label · tag path · quality chip ·
`SIM` chip · close) → state strip → hero → tabs → footer action row.

```svelte
<FaceplateShell
	label="LIT-001"
	tag="RTU9/LIT_001"
	typeName="Analog Input"
	quality={rt.quality('RTU9/LIT_001')}
	simulated={sim}
	tabs={['Value', 'Scale', 'Alarms']}
	bind:active
	chips={[{ label: 'AUTO' }, { label: 'HI-HI', kind: 'critical' }]}
	onclose={() => (open = null)}
>
	{#snippet hero()}<ScaleBar … /><Sparkline … />{/snippet}
	{#snippet panel(label)}{#if label === 'Value'}…{/if}{/snippet}
	{#snippet sim()}<WriteToggle tag="…SIMULATE" … />{/snippet}
	{#snippet footer(writable)}
		<CommandButton disabled={!writable} disabledReason="Bad quality" … />
	{/snippet}
</FaceplateShell>
```

**Two hosts, one prop.** `as="modal"` is a native `<dialog>` (focus trap, top layer and Escape for
free); `as="page"` lays the same regions out as a route, for the small-screen rule where tapping a
card navigates instead of opening a floating popup; `as="auto"` (the default) picks by viewport
width against `breakpoint` (900px). The tab strip and everything above it stay pinned — only the
panel scrolls, because a header that scrolls out of view is how an operator loses track of which
device they are commanding.

**Supplying the `sim` snippet appends a standard `Sim` tab.** Per-equipment `SIMULATE`/`SIMVALUE`
is a production feature of the controller, not a demo affordance, so every family gets the tab on
every build.

**Write-backs are gated on quality, not on existence.** The `footer` snippet receives `writable` —
`false` when the value is stale, bad or unpublished, and *`true` while simulated*, because a
simulated command is still a real command against the simulation. While `simulated`, the footer
carries a persistent `SIMULATED — not commanding the plant` banner: not a toast, because the
operator has to see it at the moment they press the button.

### The card — `EquipmentCard` and `ValueText`

Below the breakpoint a fixed coordinate plane is replaced by one wrapping grid of cards, one per
tag, each opening that equipment. Grid-friendly by construction:

```svelte
<div style="display:grid; gap:var(--space-3);
            grid-template-columns:repeat(auto-fill, minmax(min(320px,100%), 1fr))">
	{#each equipment as e (e.tag)}
		<EquipmentCard
			label={e.name}
			description={e.description}
			tag={e.tag}
			src={e.symbol}
			running={e.running}
			quality={rt.quality(e.tag)}
			values={[{ label: 'Flow', value: e.flow, units: 'gpm', precision: 0 }]}
			chips={[{ label: e.running ? 'RUN' : 'STOP', kind: e.running ? 'good' : 'off' }]}
			onopen={() => openFaceplate(e.tag)}
		/>
	{/each}
</div>
```

`ValueText` is the primitive underneath, and is worth using on its own — **it is the one place in
the kit that renders a number an operator reads, and the one place that qualifies it**:

| status | precedence | what is drawn |
| --- | --- | --- |
| not published (`present={false}`) | 1st | `—` in `--q-notpublished`, badge `NO DATA` |
| `bad` / `notConnected` | 2nd | the value in `--q-bad`, badge `BAD` |
| `simulated` | 3rd | the value in `--q-simulated`, badge `SIM` |
| `stale` | 4th | the value in `--q-stale`, badge `STALE 12m` (from `ageMs`) |
| `good` | — | the value in `--ink`, no badge at all |

The value is **never blanked** for stale or bad: it is what the plant last was, and an operator
needs it plus its age far more than a dash. Only a point the runtime does not publish loses its
number — a screen must never show a confident `0.0` for a tag that does not exist. `EquipmentCard`
carries the same fact on its **border** (solid `--q-notpublished`, dashed `--q-simulated`), which
is the legacy magenta/orange border convention re-expressed as tokens. The rules are pure functions
(`valueStatus`, `isWritable`, `formatValue`, `formatAge` in `./quality.ts`) and are unit-tested.

### Confirming — `ConfirmDialog`, `confirm()`, `AckButton`

Mount the host once, next to `<Toast/>`; then `confirm()` is an ordinary awaited function:

```svelte
<!-- +layout.svelte -->
<ConfirmDialog />
```

```ts
if (await confirm({ title: 'Stop P-101?', danger: true, operator: true })) stop();
```

Promise-based on purpose: a call site reads `if (await confirm(…))` and cannot forget to ask, which
a per-call-site `open` boolean plus a callback very much can. Requests **queue** — an operator who
triggers two confirmable actions before answering either gets both questions, in order, each with
its own promise; dropping the second would silently discard a command. Escape and the backdrop
cancel, and **the confirm button is never the focused default** — Cancel takes focus, so a stray
Enter cannot command the plant. `operator: true` shows the editable name field, prefilled from
`localStorage` and remembered: the last point before an unauthenticated, permanent record is
written.

For alarms, `AckButton` and `AlarmTable`'s `confirmAck` prop (default `false`, so nothing changes
for an existing caller) wire ack through it with the alarms enumerated **worst first**. Both paths
confirm — Ack All *and* a single row — because there is no role gate to slow an accidental click
and the record cannot be undone.

Called with no `<ConfirmDialog/>` mounted, `confirm()` warns once and falls through to the
browser's own dialog rather than returning a promise that never settles.

## Trends over history

`useTrend` is the live stream and the historian in one call. The SSE frame gives full scan
resolution from the moment the page opened; `GET /api/history` gives everything before that,
bucket-averaged server-side so a seven-day window costs the same as an hour. Neither alone is a
trend.

```svelte
<script>
	import { useTrend, Sparkline } from '@joyautomation/nautilus-hmi';
	const flow = useTrend(rt, 'WEL15_FIT_001.VALUE', { history: true, windowS: 3600 });
</script>

<Sparkline values={flow.points.map((p) => p.v)} />
```

Buffers are shared and refcounted per (client, tag, window), and the backfill re-runs on every
stream reopen — which is exactly when a gap appeared. For a window wider than a live buffer should
hold, cap `windowS` and splice a separate query on with `fetchHistory` + `mergeHistory`;
`historyAvailable()` probes `/history/span` so a runtime with no historian can say so up front
instead of failing every query. `numericLeaves(frame.tags)` enumerates every finite number the
runtime is publishing as a dotted path — build a tag picker from that and it can never offer a tag
that resolves to nothing.

## Theming

Two token layers, always: a raw palette, then a semantic alias. **A component never names a hex; it
names a role.** Override a role in your own stylesheet and the whole HMI re-skins without touching
component source.

```svelte
<script>
	import '@joyautomation/nautilus-hmi/theme.css';
	import '@joyautomation/nautilus-hmi/fonts.css'; // optional — see below
	import { onMount } from 'svelte';
	import { theme, ThemeSwitch } from '@joyautomation/nautilus-hmi';
	onMount(() => theme.init()); // theme.init('system') to follow the OS instead
</script>

<ThemeSwitch />
```

**Dark is the default.** Bare `:root` is the dark set and `theme.init()` defaults to `'dark'`; an
HMI's design case is an ops room at 03:00, and a control screen that comes up white because the
workstation happens to be set to a light desktop theme is the wrong default for the room it lives
in. A saved choice always wins. To follow the OS instead, pass `theme.init('system')`.

**Stamp the theme before paint** or a dark HMI flashes white on every load. `theme.init()` runs
after hydration, which is too late; put this in `app.html`'s `<head>`, after `%sveltekit.head%`:

```html
<script>
	(function () {
		try {
			var t = localStorage.getItem('theme');
			if (t === 'system') t = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
			document.documentElement.dataset.theme = t === 'light' ? 'light' : 'dark';
		} catch (e) {
			document.documentElement.dataset.theme = 'dark';
		}
	})();
</script>
```

### The layers

| family | tokens |
| --- | --- |
| type | `--font` (Space Grotesk) · `--mono` (IBM Plex Mono) · `--font-display` (Righteous, **chrome only** — a wordmark, never a process value) · `--font-2xs … --font-lg` |
| weight | `--weight-control` 550 · `--weight-numeric` 650 · `--weight-eyebrow` 700 |
| space | `--space-unit` `0.325rem`, and `--space-1 … --space-8` on it. Prefer these over literal px in anything that lays out; px survives only as a drawing dimension |
| radii | `--radius` 10px (panels, cards, faceplates) · `--radius-sm` 6px (controls, inputs, chips) · `--radius-pill` |
| ground | `--bg --surface --surface-2 --border --ink --ink-2 --muted --grid --axis --hover --overlay` |
| series | `--s1 --s2 --s3 --s5` — chart roles, validated per surface |
| **safety (reserved)** | `--good --warn --serious --crit`. These mean **process state and nothing else** — never chrome, never a chart series |
| brand | `--accent` (cyan-700 `#0e7490` light / cyan-500 `#06b6d4` dark) · `--primary-bg/-border/-hover` |
| depth | `--elevation-float` (the ONE elevation, for things that float) · `--tint-strength` 14% · `--wash-strength` 18% |
| motion | `--transition` .12s · `--transition-slow` .15s |

**Depth is borders and `color-mix()`, not shadows.** Exactly one elevation exists and it is for
modals, faceplates and dropdowns. Everything else is a 1px `--border` on a `--surface`. Four
utility classes ship with it: `.card` (surface + border + `--radius` + `--space-3`), `.float` (the
one elevation), `.inset` (a recessed region), and `.tint` (a wash at the house strength — set
`--tint` to the role you want).

**The eyebrow is a class.** `.eyebrow` is the house micro-label: `--font-2xs`, 700, uppercase,
`letter-spacing .06em`, `--muted`. Every section header, card description and label above a value
is one of these. `.readout` is its numeric counterpart: mono, tabular, 650 — every value on every
screen.

### Meaning-carrying colour

A legacy HMI hard-codes a handful of colours that are not taste — they are the only place a fact is
recorded. A 3.5px magenta border meant *this point is not being published*; a 3px orange one meant
*this value is simulated*. Those facts are re-expressed as roles on the reserved safety palette, so
they survive a re-skin and the palette stays four colours deep:

| token | means | default |
| --- | --- | --- |
| `--q-good` | current value, healthy source | `var(--good)` |
| `--q-stale` | was good, no longer refreshed | `var(--warn)` |
| `--q-bad` | the source says the value is wrong | `var(--crit)` |
| `--q-notpublished` | the runtime does not publish this point at all | `var(--crit)` |
| `--q-simulated` | substituted upstream of alarming — not the plant | `var(--serious)` |
| `--eq-state-run` / `-stop` / `-fault` / `-unknown` | equipment state; `unknown` is deliberately not `stop` | `--good` / `--muted` / `--crit` / `--muted` |
| `--eq-state-run-wash` | the run tint | `--good` at `--wash-strength` |

Trained on the originals? `--q-notpublished: #ff00ff; --q-simulated: #ff8c00;` restores them.

### Fonts

`theme.css` **names** the three faces with full fallback stacks but does not load them, so a host
app with its own typography pays nothing. Import `fonts.css` as well and they are served from your
own bundle via `@fontsource` — **never a Google Fonts `<link>`**. An HMI runs on an isolated OT
network as often as not, and a stylesheet that reaches out to a CDN is a screen that renders in
Times New Roman on the day it matters. Space Grotesk is the variable cut (the house weights are
half-steps); IBM Plex Mono is pulled at 400/500/600/700; Righteous is one weight, because it is
chrome only.

### Legacy-port token families

The legacy-port components carry a second, narrower set of tokens, because a 1:1 recreation has to
reproduce colours the source package hard-coded rather than themed. Each falls back to a theme
token, so setting none of them still yields a coherent light/dark component:

| tokens | used by |
| --- | --- |
| `--eq-tint`, `--eq-sim`, `--eq-chip-bg`, `--eq-chip-ink` | `EquipSymbol` |
| `--bar-normal`, `--bar-warn`, `--bar-trip`, `--bar-outline`, `--bar-track`, `--bar-indicator` | `ScaleBar` |
| `--row-on`, `--row-off`, `--row-fault`, `--row-unknown`, `--row-ink`, `--row-chip-bg`, `--row-chip-ink`, `--row-value-fault` | `StatusRow` |
| `--glyph-stroke`, `--glyph-fill`, `--glyph-body`, `--glyph-off`, `--glyph-on`, `--glyph-mark`, `--glyph-nodata` | `TankGlyph`, `PumpGlyph`, `ValveGlyph` |
| `--flow-wire`, `--flow-color`, `--flow-hover` | `FlowLink` |

`theme.css` also defines one unscoped rule, `.blinking`, for an attention flash on an
unacknowledged condition (`EquipSymbol`'s fault bell sets it). It is deliberately not
component-scoped so a host app can redefine it, and the reduced-motion rule replaces it with an
outline rather than dropping the indication. The `motion` store / `MotionSwitch` do the same for
reduced-motion via `data-motion`.

## Components (props)

| Component | Key props |
| --- | --- |
| `Tank` | `levelPct`, `tempC`, `heaterPct`, `label`, `width` |
| `LevelTank` | `value`, `min`, `max`, `units`, `precision`, `label`, `marks`, `overflow`, `volume`, `fill`, `width`, `height` |
| `Gauge` | `value`, `min`, `max`, `unit`, `label`, `color`, `setpoint`, `decimals`, `width`, `sweep`, `gap`, `thickness` |
| `ScaleBar` | `value`, `min`, `max`, `hh`/`h`/`l`/`ll` (`null` = limit disabled), `bands`, `orientation`, `thickness`, `length`, `showScale`, `indicatorColor`, `label`, `units`, `precision` |
| `EquipSymbol` | `src`, `width`, `height`, `running`, `auto`, `remote`, `fault`, `simulate`, `comFail`, `stateText`, `label`, `mirror`, `showChips`, `onclick`, `extra` |
| `StatusRow` | `state: 'on' \| 'off' \| 'fault' \| 'unknown'`, `label`, `auto`/`remote` (`null` hides the chip), `wide`, `valueFault`, `height`, `title`, `value` (snippet) |
| `CoordinateCanvas` | `spec: { width, height, items }`, `registry`, `leaf` (snippet), `fit`, `containers`, `graphics`, `stretch`, `visible`, `style`, `href`, `fontFamily`, `fontSize`, `color` |
| `TankGlyph` | `x`, `y`, `w`, `h`, `level` (0–1, `undefined` = no data), `fill`, `marks`, `id`, `corner`, `value`, `sub` — SVG `<g>` |
| `PumpGlyph` | `cx`, `cy`, `r`, `label`, `value`, `running`, `nodata`, `disabled` — SVG `<g>` |
| `ValveGlyph` | `cx`, `cy`, `rx`, `ry`, `label`, `value`, `open`, `nodata` — SVG `<g>` |
| `FlowLink` | `d`, `flowing`, `dead`, `dashed`, `color`, `width`, `onmousemove`, `onmouseleave`, `onclick` — SVG `<g>` |
| `WriteNumber` | `tag`, `label`, `value`, `present`, `units`, `precision`, `step`, `min`, `max`, `readonly`, `readonlyReason`, `write(name, value)` |
| `WriteToggle` | `tag`, `label`, `value`, `present`, `onColor`, `offColor`, `alarm`, `invert`, `readonly`, `readonlyReason`, `write(name, value)` |
| `CommandButton` | `tag`, `label`, `kind`, `pulseMs`, `held`, `disabled`, `disabledReason`, `write(name, value)` |
| `Trend` | `series: { name, color, points, dashed? }[]`, `unit`, `height`, `yMin`, `yMax`, `windowS` |
| `Pump` | `running`, `speedPct`, `label`, `width` |
| `Valve` | `openPct`, `label`, `width` |
| `Pipe` | `d` (SVG path), `flowing`, `rate`, `color` — render inside an `<svg>` |
| `StatusPill` | `kind: 'good' \| 'warning' \| 'serious' \| 'critical' \| 'off'`, `label` |
| `Sparkline` | `values: number[]`, `color`, `height`, `yMin`, `yMax`, `endIndex`, `windowSize`, `compact` |
| `TrendChart` | `pens: TrendPen[]`, `thresholds?: TrendThreshold[]` (chart-level when `penId` is omitted, `shared` mode only), `height`, `windowMs`, `axisMode: 'shared' \| 'percent'`, `yMin`, `yMax`, `yLabel`, `gapMs`, `bind:paused` |
| `Histogram` | `counts: number[]`, `bucketWidth`, `unit`, `height`, `color` |
| `NumberField` | `label`, `unit`, `value`, `min`, `max`, `step`, `onsubmit(v)` |
| `AlarmBanner` | `summary: AlarmSummary`, `now`, `onclick?`, `href?` |
| `AlarmTable` | `alarms: AlarmInstance[]`, `sites?`, `now`, `onack(ids,by)`, `onshelve(id,until,by)`, `onunshelve?(id,by)`, `shelveTimes?`, `operator?`, `onselect?(instance)`, `pageSize?`, `confirmAck?` |
| `AckButton` | `alarms?: AlarmInstance[]`, `ids?` (`['*']` = ack all), `onack(ids,by)`, `label?`, `operator?`, `confirmAck?`, `variant?`, `size?`, `icon?`, `disabled?` |
| `FaceplateShell` | `label`, `tag?`, `typeName?`, `quality?`, `present?`, `simulated?`, `showQuality?`, `as: 'modal' \| 'page' \| 'auto'`, `breakpoint?`, `size?`, `tabs?`, `bind:active`, `simTab?`, `chips?`, `closeOnEscape?`, `closeOnBackdrop?`, `simNote?`, `onclose`, snippets `hero` / `status` / `panel(label, i)` / `sim` / `children` / `footer(writable)` |
| `EquipmentCard` | `label`, `description?`, `tag?`, `src?`/`symbol` (snippet), `symbolWidth?`, `symbolHeight?`, `running?`, `fault?`, `auto?`, `remote?`, `stateText?`, `quality?`, `present?`, `simulated?`, `values?: CardValue[]`, `chips?: CardChip[]`, `onopen?`/`href?`, snippets `sparkline` / `extra` |
| `ValueText` | `value`, `units?`, `precision?`, `quality?`, `simulated?`, `present?`, `ageMs?`, `label?`, `size?`, `align?`, `placeholder?`, `trueText?`/`falseText?`, `showBadge?` |
| `StateChip` | `label`, `kind?: StatusKind \| ValueStatus \| 'neutral'`, `title?`, `dot?`, `solid?` |
| `ConfirmDialog` | `bind:operator?`, `size?` — mount once; drive it with `confirm({ title, body?, items?, confirmLabel?, cancelLabel?, danger?, operator?, note? })` |
| `AlarmJournal` | `events: AlarmEvent[]`, `from`, `to`, `onrange(from,to)`, `sites?`, `loading?` |
| `CatalogIndex` | `groups: StoryGroup[]`, `title?`, `blurb?`, `basePath?`, `href?(story,group)`, `search?`, `bind:query?`, `searchPlaceholder?`, `minColumn?`, `previewHeight?`, `showGroupHeadings?`, snippets `preview(story,group)` / `empty(query)` |
| `CatalogEntry` | `story?: Story`, `group?`, `backPath?`, `backLabel?`, `prev?`/`next?`, `basePath?`, `href?(story)`, `showProps?`, `propsLabel?`, `maxArray?`, `minColumn?`, `stageMinHeight?`, snippet `notFound` |

### The component catalog — `CatalogIndex`, `CatalogEntry`, `./catalog.ts`

A storybook for a plant. The kit ships the *shape* and the two screens; the
**registry is the app's**, because a deployment's symbols and process systems
are none of the kit's business. Two route files and one `stories.ts`:

```ts
// src/lib/stories.ts — grouped by PROCESS SYSTEM, not by component taxonomy:
// that is the axis an engineer is thinking on when they come looking.
import type { StoryGroup } from '@joyautomation/nautilus-hmi';
import LevelTank from '…';

export const groups: StoryGroup[] = [
  {
    id: 'reservoirs',
    title: 'Reservoirs',
    stories: [
      {
        slug: 'level-tank',
        title: 'Level tank',
        blurb: 'Level-only vessel; the band marks are the alarm limits.',
        component: LevelTank,
        variants: [
          { name: 'Normal', props: { value: 21.4, min: 0, max: 30, units: 'ft' } },
          { name: 'Low-low', props: { value: 2.1, min: 0, max: 30, units: 'ft' },
            note: 'Below LL the fill goes critical, not merely red.' }
        ]
      },
      // No `component`: this one reads its own tags, so it has no static state.
      { slug: 'zone-box', title: 'Zone box', blurb: 'Panel rollup tile.',
        note: 'Takes an RTU id and renders from the live subscription.' }
    ]
  }
];
```

```svelte
<!-- routes/components/+page.svelte -->
<script lang="ts">
  import { CatalogIndex } from '@joyautomation/nautilus-hmi';
  import { groups } from '$lib/stories';
</script>
<CatalogIndex {groups} search />
```

```svelte
<!-- routes/components/[slug]/+page.svelte -->
<script lang="ts">
  import { page } from '$app/state';
  import { CatalogEntry, findStory, neighbors } from '@joyautomation/nautilus-hmi';
  import { groups } from '$lib/stories';
  const hit = $derived(findStory(groups, page.params.slug));
  const near = $derived(neighbors(groups, page.params.slug));
</script>
<CatalogEntry story={hit?.story} group={hit?.group} {...near} />
```

**A variant renders from static props alone.** That is the one rule, and it is
what makes the catalog useful during a comms failure rather than blank. A
component that reads its own data cannot honour it, so `Story.component` is
optional: leave it off and add a `note`, and the story renders as a **live-only
card** — still named, still grouped, still searchable, saying why it has no
preview. That is information. A broken box is not, and hiding the component
from the list is worse than either.

## Testing

Most of this kit is Svelte components whose test is looking at them. The pure
logic that can *silently mislead an operator* has real tests, run with no
dependencies: the delta-frame merge (`delta.ts`), the quality precedence and
value formatting rules (`quality.ts`), and the confirm queue's promise flow
(`confirm.ts`) — each one a place where a bug shows up as a plausible screen
rather than as an error.

```sh
npm test
```

`tests/harness.ts` is a small stand-in for vitest (a strict subset of its API),
so the kit tests its own logic without acquiring a test runner. If it ever
adopts one, each spec changes a single import line. A spec may be `async`, as
long as everything it awaits is microtask-only: the harness has no runner loop
and reports an async spec that never settled as a failure rather than letting it
disappear into a green run.

## Building the package

```sh
npm install
npm run package   # svelte-kit sync && svelte-package  → ./dist
npm run check     # type-check with svelte-check
```

## License

MIT © Joy Automation
