<script lang="ts" module>
	import type { StatusKind as _StatusKind } from '../types.js';
	import type { ValueStatus as _ValueStatus } from '../quality.js';

	/**
	 * `StatusKind` covers process state (good/warning/serious/critical/off);
	 * `ValueStatus` covers data quality (stale/bad/notPublished/simulated), so
	 * a quality chip and a state chip are the same component. `neutral` is
	 * chrome — an unstyled fact like a mode or a family name.
	 */
	export type ChipKind = _StatusKind | _ValueStatus | 'neutral';
</script>

<script lang="ts">
	// The house state chip: --font-2xs, pill radius, 1px × --space-1 padding,
	// colour from the reserved safety set. One line of state, tiled by the
	// dozen across a card's chip row and a faceplate's status strip.
	//
	// Smaller and denser than `StatusPill` on purpose — that one is a labelled
	// indicator with an icon and a border, sized for a control panel; this one
	// is a tag on a card, sized for eight of them in a 320px box. Colour is
	// never the only cue: the label always says the state in words.
	import { STATUS_META, type ValueStatus } from '../quality.js';

	let {
		label,
		kind = 'neutral',
		title,
		dot = true,
		solid = false
	}: {
		label: string;
		kind?: ChipKind;
		/** Hover text — defaults to the quality description where there is one. */
		title?: string;
		/** The colour dot. Off for a chip whose label is already the colour's word. */
		dot?: boolean;
		/** Fill the chip with the tint instead of tinting only the dot. */
		solid?: boolean;
	} = $props();

	const COLORS: Record<ChipKind, string> = {
		good: 'var(--good)',
		warning: 'var(--warn)',
		serious: 'var(--serious)',
		critical: 'var(--crit)',
		off: 'var(--muted)',
		neutral: 'var(--muted)',
		stale: 'var(--q-stale)',
		bad: 'var(--q-bad)',
		notPublished: 'var(--q-notpublished)',
		simulated: 'var(--q-simulated)'
	};

	const color = $derived(COLORS[kind] ?? 'var(--muted)');
	const hint = $derived(
		title ?? (kind in STATUS_META ? STATUS_META[kind as ValueStatus].description : undefined)
	);
</script>

<span class="chip" class:solid class:muted={kind === 'neutral' || kind === 'off'} style="--c: {color}" title={hint}>
	{#if dot}<span class="dot" aria-hidden="true"></span>{/if}
	{label}
</span>

<style>
	.chip {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		padding: 1px var(--space-1);
		border-radius: var(--radius-pill);
		border: 1px solid color-mix(in srgb, var(--c) 45%, var(--border));
		background: transparent;
		color: var(--c);
		font-size: var(--font-2xs);
		font-weight: var(--weight-control);
		letter-spacing: 0.02em;
		line-height: 1.5;
		white-space: nowrap;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	/* A neutral/off chip is a fact, not a signal — it must not read as loud as
	   the four safety colours sitting next to it. */
	.chip.muted {
		color: var(--ink-2);
		border-color: var(--border);
	}
	.chip.solid {
		background: color-mix(in srgb, var(--c) var(--tint-strength), transparent);
	}
	.dot {
		flex: none;
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--c);
	}
</style>
