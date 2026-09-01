<script lang="ts">
	// The quality-aware value primitive: ONE place in the kit renders a number
	// an operator reads, and one place decides how it is qualified.
	//
	// Before this, a screen could show a confident `12.4 ft` for a tag that had
	// not updated in an hour, or a plausible `0.0` for a point the runtime does
	// not publish at all. `ValueText` makes both impossible: it takes the value
	// AND the two facts about it (`quality` from `RealtimeClient.quality(tag)`,
	// `simulated` from the equipment's own SIMULATE member) and renders the
	// indication once, in the house idiom —
	//
	//   not published  →  `—` in --q-notpublished, badge "NO DATA"
	//   bad            →  the value greyed, in --q-bad, badge "BAD"
	//   simulated      →  the value in --q-simulated, badge "SIM"
	//   stale          →  the value in --q-stale, badge "STALE 12m"
	//   good           →  the value, in --ink, no badge at all
	//
	// The value is NEVER blanked for stale or bad. It is what the plant last
	// was, and an operator needs it plus its age far more than a dash.
	import { formatAge, formatValue, STATUS_META, valueStatus } from '../quality.js';
	import type { Quality } from '../types.js';

	let {
		value,
		units = '',
		precision = 1,
		quality = 'good',
		simulated = false,
		present = true,
		ageMs,
		label = '',
		size = 'md',
		align = 'start',
		placeholder = '—',
		trueText = 'On',
		falseText = 'Off',
		showBadge = true,
		title
	}: {
		/** The live readback. `undefined`/`null`/`NaN` render as `placeholder`. */
		value?: number | string | boolean | null;
		units?: string;
		/** Decimal places for a numeric value. */
		precision?: number;
		/** From `RealtimeClient.quality(tag)`. */
		quality?: Quality;
		/** The equipment's own SIMULATE member — the value is not the plant's. */
		simulated?: boolean;
		/** False when the runtime does not publish this tag at all. */
		present?: boolean;
		/** Age of the reading, for the stale badge. Omitted → badge with no age. */
		ageMs?: number;
		/** Optional eyebrow above the value. */
		label?: string;
		size?: 'sm' | 'md' | 'lg';
		align?: 'start' | 'end';
		placeholder?: string;
		trueText?: string;
		falseText?: string;
		/** Hide the badge where the surrounding chrome already carries quality. */
		showBadge?: boolean;
		title?: string;
	} = $props();

	const status = $derived(valueStatus({ present, quality, simulated }));
	const meta = $derived(STATUS_META[status]);
	const shown = $derived(
		formatValue(status === 'notPublished' ? null : value, { precision, placeholder, trueText, falseText })
	);
	const age = $derived(formatAge(ageMs));
	const badge = $derived(
		status === 'stale' && age ? `${meta.label} ${age}` : meta.label.toUpperCase()
	);
</script>

<span
	class="v {size} {status}"
	class:end={align === 'end'}
	style="--q: {meta.token}"
	title={title ?? (status === 'good' ? undefined : meta.description)}
>
	{#if label}<span class="eyebrow lbl">{label}</span>{/if}
	<span class="line">
		<span class="num" class:absent={!shown.present}>{shown.text}</span>
		{#if units && shown.present}<span class="unit">{units}</span>{/if}
		{#if showBadge && meta.degraded}
			<span class="badge">{badge}</span>
		{/if}
	</span>
</span>

<style>
	.v {
		display: inline-flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}
	.v.end {
		align-items: flex-end;
	}
	.lbl {
		/* .eyebrow is global (theme.css); this only pins the truncation. */
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.line {
		display: inline-flex;
		align-items: baseline;
		gap: var(--space-1);
		min-width: 0;
	}
	.num {
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-weight: var(--weight-numeric);
		color: var(--ink);
		line-height: 1.2;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.sm .num {
		font-size: var(--font-xs);
	}
	.md .num {
		font-size: var(--font-md);
	}
	.lg .num {
		font-size: var(--font-lg);
	}
	.unit {
		font-size: var(--font-2xs);
		color: var(--muted);
		white-space: nowrap;
	}

	/* A qualified value takes the quality colour. `good` is the only status
	   that leaves the readout in plain --ink, which is what makes a coloured
	   number mean something. */
	.stale .num,
	.bad .num,
	.simulated .num,
	.notPublished .num {
		color: var(--q);
	}
	/* Not published: there is no number, so the dash carries the colour and
	   the badge carries the word. */
	.num.absent {
		font-weight: var(--weight-control);
	}

	.badge {
		flex: none;
		font-size: var(--font-2xs);
		font-weight: var(--weight-eyebrow);
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--q);
		border: 1px solid color-mix(in srgb, var(--q) 45%, var(--border));
		background: color-mix(in srgb, var(--q) var(--tint-strength), transparent);
		border-radius: var(--radius-pill);
		padding: 0 var(--space-1);
		line-height: 1.5;
		white-space: nowrap;
	}
</style>
