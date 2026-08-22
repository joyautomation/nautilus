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

## Theming

Every component reads tokens from `theme.css`. Flip the whole HMI between light and dark by stamping
`data-theme="light" | "dark"` on `<html>` — the bundled `theme` store does this for you and persists
the choice:

```svelte
<script>
	import { onMount } from 'svelte';
	import { theme, ThemeSwitch } from '@joyautomation/nautilus-hmi';
	onMount(() => theme.init());
</script>

<ThemeSwitch />
```

Override any token (e.g. `--s1`, `--surface`, `--accent`) in your own stylesheet to rebrand without
touching component source. The `motion` store / `MotionSwitch` do the same for reduced-motion via
`data-motion`.

## Components (props)

| Component | Key props |
| --- | --- |
| `Tank` | `levelPct`, `tempC`, `heaterPct`, `label`, `width` |
| `Gauge` | `value`, `min`, `max`, `unit`, `label`, `color`, `setpoint`, `decimals`, `width` |
| `Trend` | `series: { name, color, points, dashed? }[]`, `unit`, `height`, `yMin`, `yMax`, `windowS` |
| `Pump` | `running`, `speedPct`, `label`, `width` |
| `Valve` | `openPct`, `label`, `width` |
| `Pipe` | `d` (SVG path), `flowing`, `rate`, `color` — render inside an `<svg>` |
| `StatusPill` | `kind: 'good' \| 'warning' \| 'serious' \| 'critical' \| 'off'`, `label` |
| `Sparkline` | `values: number[]`, `color`, `height`, `yMin`, `yMax` |
| `Histogram` | `counts: number[]`, `bucketWidth`, `unit`, `height`, `color` |
| `NumberField` | `label`, `unit`, `value`, `min`, `max`, `step`, `onsubmit(v)` |
| `AlarmBanner` | `summary: AlarmSummary`, `now`, `onclick?`, `href?` |
| `AlarmTable` | `alarms: AlarmInstance[]`, `sites?`, `now`, `onack(ids,by)`, `onshelve(id,until,by)`, `onunshelve?(id,by)`, `shelveTimes?`, `operator?`, `onselect?(instance)`, `pageSize?` |
| `AlarmJournal` | `events: AlarmEvent[]`, `from`, `to`, `onrange(from,to)`, `sites?`, `loading?` |

## Building the package

```sh
npm install
npm run package   # svelte-kit sync && svelte-package  → ./dist
npm run check     # type-check with svelte-check
```

## License

MIT © Joy Automation
