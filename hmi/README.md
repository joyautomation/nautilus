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

## Building the package

```sh
npm install
npm run package   # svelte-kit sync && svelte-package  → ./dist
npm run check     # type-check with svelte-check
```

## License

MIT © Joy Automation
