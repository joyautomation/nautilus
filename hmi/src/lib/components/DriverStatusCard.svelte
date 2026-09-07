<script lang="ts">
	// A field driver's / publisher's health card: connection state, a human
	// sentence, uptime, a labeled metrics grid, and — for a Sparkplug node —
	// its devices. Feed it one `DriverStatus` from GET /api/drivers (or an
	// entry of `frame.drivers`). Protocol-agnostic: it renders whatever
	// metrics and devices the status carries.
	import ConnectionBadge from './ConnectionBadge.svelte';
	import type { DriverStatus, DriverMetric } from '../types.js';

	let { driver, now = Date.now() }: { driver: DriverStatus; now?: number } = $props();

	// A friendly protocol label + accent for the kind chip.
	const KIND: Record<string, { label: string; color: string }> = {
		'ethernet-ip': { label: 'EtherNet/IP', color: 'var(--s1)' },
		sparkplug: { label: 'Sparkplug B', color: 'var(--accent)' }
	};
	const kind = $derived(KIND[driver.kind] ?? { label: driver.kind, color: 'var(--muted)' });

	const uptime = $derived.by(() => {
		if (!driver.sinceMs || driver.state !== 'connected') return '';
		const s = Math.max(0, Math.floor((now - driver.sinceMs) / 1000));
		if (s < 60) return `${s}s`;
		if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
		if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
		return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
	});

	// Ages (`atMs`) are measured against the status's OWN observation time,
	// not the wall clock. On a delta stream the controller sends this block
	// only when it changes, so a healthy driver's status can be seconds old
	// — and rendering "last publish" against `now` would show a plant going
	// quiet every time nothing happened. `asOfMs` is what the server
	// stamped; without it (an older controller) the clock is the best
	// available answer, and that controller re-sent the block every frame
	// anyway. Uptime stays on `now`: `sinceMs` is an absolute start.
	const asOf = $derived(driver.asOfMs || now);

	const ago = (ms: number) => {
		const s = Math.max(0, ms) / 1000;
		if (s < 10) return `${s.toFixed(1)}s`;
		if (s < 60) return `${Math.round(s)}s`;
		if (s < 3600) return `${Math.round(s / 60)}m`;
		return `${Math.round(s / 3600)}h`;
	};

	const fmt = (m: DriverMetric) => {
		if (m.atMs) return ago(asOf - m.atMs);
		if (m.text) return m.text;
		const v = m.value;
		const n = Number.isInteger(v) ? v.toLocaleString() : v.toFixed(2);
		return m.unit ? `${n} ${m.unit}` : n;
	};
</script>

<div class="card" class:bad={driver.state === 'error' || driver.state === 'degraded'}>
	<div class="head">
		<div class="title">
			<span class="name">{driver.name}</span>
			<span class="kind" style="--k: {kind.color}">{kind.label}</span>
		</div>
		<ConnectionBadge state={driver.state} />
	</div>

	<p class="detail">{driver.detail}</p>
	<p class="msg">{driver.message}{#if uptime}<span class="uptime"> · up {uptime}</span>{/if}</p>

	{#if driver.lastError}
		<p class="err" title={driver.lastError}>{driver.lastError}</p>
	{/if}

	{#if driver.metrics?.length}
		<div class="metrics">
			{#each driver.metrics as m (m.label)}
				<div class="metric">
					<span class="mv">{fmt(m)}</span>
					<span class="ml">{m.label}</span>
				</div>
			{/each}
		</div>
	{/if}

	{#if driver.devices?.length}
		<div class="devices">
			<span class="dhead">devices</span>
			{#each driver.devices as d (d.id)}
				<div class="device">
					<ConnectionBadge state={d.online ? 'connected' : 'offline'} label={d.id} size="sm" />
					{#if d.detail}<span class="ddetail">{d.detail}</span>{/if}
				</div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.card {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 8px);
		padding: 14px 16px;
		display: grid;
		/* minmax(0,1fr) is load-bearing: a single implicit `auto` column
		   sizes to the content's max-content and overflows; a 1fr track is
		   constrained to the card width, so long names/metrics stay inside. */
		grid-template-columns: minmax(0, 1fr);
		gap: 8px;
		min-width: 260px;
		overflow: hidden;
	}
	.card.bad {
		border-color: color-mix(in srgb, var(--crit) 40%, var(--border));
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		min-width: 0;
	}
	.title {
		display: flex;
		align-items: baseline;
		/* The kind chip drops under the name when the two will not share a
		   line — on a phone the name is the fact, the chip is the footnote. */
		flex-wrap: wrap;
		gap: 2px 8px;
		min-width: 0;
	}
	.name {
		font-weight: 650;
		font-size: var(--font-md);
		color: var(--ink);
		font-family: var(--mono);
		/* Two lines, then ellipsis. A one-line name on a 390 px screen was
		   "Pomo…" next to a chip and a pill at full width. */
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
		overflow-wrap: anywhere;
		min-width: 0;
	}
	.kind {
		font-size: var(--font-2xs);
		font-weight: 700;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--k);
		border: 1px solid color-mix(in srgb, var(--k) 40%, var(--border));
		border-radius: 4px;
		padding: 1px 5px;
		flex: none;
	}
	.detail {
		margin: 0;
		font-size: var(--font-2xs);
		color: var(--muted);
		font-family: var(--mono);
	}
	.msg {
		margin: 0;
		font-size: var(--font-xs);
		color: var(--ink-2);
	}
	.uptime {
		color: var(--muted);
	}
	.err {
		margin: 0;
		font-size: var(--font-2xs);
		color: var(--crit);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.metrics {
		display: grid;
		/* min-width:0 lets the grid + tiles shrink to the card width; without
		   it, nowrap tiles size to their intrinsic width and overflow. */
		grid-template-columns: repeat(auto-fit, minmax(72px, 1fr));
		gap: 8px;
		margin-top: 2px;
		min-width: 0;
	}
	.metric {
		background: var(--surface-2);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 6px 8px;
		/* Floored height + non-wrapping VALUE: a metric whose text changes
		   each frame (a freshness time) must never reflow the card. The
		   caption is static, so it may wrap once and set the height. */
		min-height: 44px;
		min-width: 0;
		display: flex;
		flex-direction: column;
		justify-content: center;
		gap: 1px;
		overflow: hidden;
	}
	.mv {
		font-family: var(--mono);
		font-weight: 650;
		font-size: var(--font-sm);
		color: var(--ink);
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.ml {
		font-size: var(--font-2xs);
		color: var(--muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		line-height: 1.25;
		overflow-wrap: anywhere;
	}
	.devices {
		display: grid;
		gap: 6px;
		margin-top: 2px;
		border-top: 1px solid var(--border);
		padding-top: 8px;
	}
	.dhead {
		font-size: var(--font-2xs);
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--muted);
	}
	.device {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.ddetail {
		font-size: var(--font-2xs);
		color: var(--muted);
		font-family: var(--mono);
	}
</style>
