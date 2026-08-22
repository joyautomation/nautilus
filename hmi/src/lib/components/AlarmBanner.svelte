<script lang="ts">
	// Top-bar alarm summary — the `Header/Alarm_Active` pulsing style class
	// from the Perspective view: counts by priority (worst first), the unack
	// count, and the newest unacknowledged alarm's name. Feed it
	// `frame.alarms` (or `alarms.summary` from `createAlarmClient`) straight
	// off the stream — no fetching here, same house rule as
	// `DriverStatusPanel`. Flashes via CSS while `unacked > 0`; respects
	// reduced-motion.
	import type { AlarmSummary, Priority } from '../alarms.svelte.js';
	import { PRIORITY_META, PRIORITY_ORDER } from '../alarms.svelte.js';

	let {
		summary,
		now = Date.now(),
		onclick,
		href
	}: {
		/** `null`/`undefined` while unknown (before the first frame, or a
		 * controller with no alarm engine) — renders nothing. */
		summary?: AlarmSummary | null;
		/** Epoch ms "now" for the newest-alarm age readout. */
		now?: number;
		/** Click handler — set this or `href`, not both. */
		onclick?: () => void;
		/** Renders as an `<a>` instead of a `<button>` when set. */
		href?: string;
	} = $props();

	const byPriority = $derived.by(() => {
		if (!summary) return [];
		return [...PRIORITY_ORDER]
			.reverse()
			.map((p) => ({ p, n: summary.byPriority[p] ?? 0 }))
			.filter((x) => x.n > 0);
	});

	const newestAge = $derived.by(() => {
		if (!summary?.newest) return '';
		const s = Math.max(0, Math.floor((now - summary.newest.ms) / 1000));
		if (s < 60) return `${s}s ago`;
		if (s < 3600) return `${Math.floor(s / 60)}m ago`;
		return `${Math.floor(s / 3600)}h ago`;
	});

	const worstColor = $derived(summary?.worst ? PRIORITY_META[summary.worst].color : 'var(--muted)');
</script>

{#snippet content()}
	<span class="dot" aria-hidden="true"></span>

	<span class="unacked">
		<b>{summary?.unacked}</b> unack
	</span>

	<span class="counts">
		{#each byPriority as { p, n } (p)}
			<span class="count" style="--c: {PRIORITY_META[p].color}" title={PRIORITY_META[p].label}>
				<span class="glyph" aria-hidden="true">{PRIORITY_META[p].glyph}</span>{n}
			</span>
		{/each}
		{#if !byPriority.length}
			<span class="count clear">no active alarms</span>
		{/if}
	</span>

	{#if summary?.newest}
		<span class="newest">
			<span class="name">{summary.newest.name}</span>
			<span class="age">{newestAge}</span>
		</span>
	{/if}

	{#if summary?.shelved}
		<span class="shelved" title="{summary.shelved} shelved">{summary.shelved} shelved</span>
	{/if}
{/snippet}

{#if summary}
	{#if href}
		<a
			{href}
			class="banner"
			class:flash={summary.unacked > 0}
			style="--worst: {worstColor}"
			{onclick}
			aria-live={summary.unacked > 0 ? 'assertive' : 'polite'}
		>
			{@render content()}
		</a>
	{:else}
		<button
			type="button"
			class="banner"
			class:flash={summary.unacked > 0}
			style="--worst: {worstColor}"
			{onclick}
			aria-live={summary.unacked > 0 ? 'assertive' : 'polite'}
		>
			{@render content()}
		</button>
	{/if}
{/if}

<style>
	.banner {
		display: flex;
		align-items: center;
		gap: 14px;
		width: 100%;
		padding: 8px 14px;
		background: var(--surface);
		border: 1px solid color-mix(in srgb, var(--worst) 35%, var(--border));
		border-radius: var(--radius, 8px);
		color: var(--ink);
		font: inherit;
		text-align: left;
		text-decoration: none;
		cursor: pointer;
	}
	.banner:hover {
		background: var(--hover, var(--surface-2));
	}
	.banner:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.banner.flash {
		animation: flash 1.1s ease-in-out infinite;
	}
	@keyframes flash {
		0%,
		100% {
			border-color: color-mix(in srgb, var(--worst) 35%, var(--border));
			box-shadow: none;
		}
		50% {
			border-color: var(--worst);
			box-shadow: 0 0 0 1px color-mix(in srgb, var(--worst) 45%, transparent);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.banner.flash {
			animation: none;
			border-color: var(--worst);
		}
	}
	.dot {
		flex: none;
		width: 10px;
		height: 10px;
		border-radius: 50%;
		background: var(--worst);
		box-shadow: 0 0 6px color-mix(in srgb, var(--worst) 70%, transparent);
	}
	.unacked {
		flex: none;
		font-size: var(--font-sm);
		color: var(--ink);
	}
	.unacked b {
		font-family: var(--mono);
		font-size: var(--font-md);
	}
	.counts {
		display: flex;
		gap: 10px;
		flex: none;
	}
	.count {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		font-family: var(--mono);
		font-size: var(--font-xs);
		font-weight: 650;
		color: var(--c);
	}
	.count.clear {
		font-family: inherit;
		font-weight: 500;
		color: var(--muted);
	}
	.glyph {
		font-size: 10px;
	}
	.newest {
		display: flex;
		align-items: baseline;
		gap: 8px;
		min-width: 0;
		flex: 1;
		border-left: 1px solid var(--border);
		padding-left: 12px;
	}
	.newest .name {
		font-size: var(--font-xs);
		color: var(--ink-2);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.newest .age {
		flex: none;
		font-size: var(--font-2xs);
		color: var(--muted);
	}
	.shelved {
		flex: none;
		font-size: var(--font-2xs);
		color: var(--muted);
	}
</style>
