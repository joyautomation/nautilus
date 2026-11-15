<script lang="ts">
	// A machine as an ISA-101 screen draws one: the ISA symbol for its kind
	// (a pump, a blower, a compressor, an agitator), its tag number beside it
	// and its state in words. Running is the symbol filled; stopped is the
	// outline; faulted is drawn in the alarm colour with the word. A speed,
	// when given, follows the state ("Running 72 %").
	//
	// Pipes meet the circle on any side (ports `left`, `right`, `top`,
	// `bottom`): a pump's suction is its left, its discharge its top; a
	// blower's or compressor's inlet its left, outlet its right. An agitator
	// has no pipes: place it over its tank, the shaft running down into it.
	import type { MimicLook } from '../mimic.js';

	let {
		kind = 'pump',
		tag = '',
		running = false,
		faulted = false,
		speedPct,
		label,
		width,
		height,
		look = 'isa101'
	}: {
		kind?: 'pump' | 'blower' | 'compressor' | 'agitator';
		/** Its tag number, P-501. */
		tag?: string;
		running?: boolean;
		faulted?: boolean;
		/** Speed in percent, for a machine on a drive. */
		speedPct?: number | null;
		/** The equipment's label, used as the tag when `tag` is empty. */
		label?: string;
		width?: number;
		height?: number;
		look?: MimicLook;
	} = $props();

	const name = $derived(tag || label || '');
	const state = $derived.by(() => {
		const s = faulted ? 'Faulted' : running ? 'Running' : 'Stopped';
		return typeof speedPct === 'number' && Number.isFinite(speedPct) && !faulted ? `${s} ${Math.round(speedPct)} %` : s;
	});
	const R = 20;
	const shaft = $derived(kind === 'agitator' ? 46 : 0);
	const textW = $derived(Math.max(name.length, state.length) * 7.5);
	const w = $derived(width ?? 2 * R + 4 + 8 + textW);
	const h = $derived(height ?? Math.max(2 * R + 4, 34) + shaft);
	const cx = R + 2;
	const cy = R + 2;
	const ink = $derived(faulted ? 'var(--crit, #e5484d)' : 'var(--ink-2, #c3c2b7)');
	const fill = $derived(running && !faulted ? 'var(--ink-2, #c3c2b7)' : 'var(--surface, #161615)');
	const mark = $derived(running && !faulted ? 'var(--bg, #0d0d0d)' : ink);
</script>

<svg viewBox="0 0 {w} {h}" width={w} height={h} role="img" aria-label={`${kind} ${name}: ${state}`}>
	<rect data-part="body" x={cx - R} y={cy - R} width={2 * R} height={2 * R} fill="none" />
	{#if kind === 'agitator'}
		<!-- the motor, the shaft down into the tank, the impeller -->
		<line x1={cx} y1={cy + R} x2={cx} y2={cy + R + shaft - 6} stroke={ink} stroke-width="2.5" />
		<path d="M {cx - 16} {cy + R + shaft - 6} L {cx + 16} {cy + R + shaft - 6} M {cx - 16} {cy + R + shaft - 11} L {cx - 16} {cy + R + shaft - 1} M {cx + 16} {cy + R + shaft - 11} L {cx + 16} {cy + R + shaft - 1}" fill="none" stroke={ink} stroke-width="2.5" stroke-linecap="round" />
	{/if}
	<circle {cx} {cy} r={R} {fill} stroke={ink} stroke-width="2" />
	{#if kind === 'pump'}
		<!-- the ISA centrifugal pump: a discharge tangent off the top, a triangle for the flow -->
		<path d="M {cx} {cy - R} L {cx + R} {cy - R}" stroke={ink} stroke-width="2" />
		<path d="M {cx - 9} {cy - 10} L {cx + 11} {cy} L {cx - 9} {cy + 10} Z" fill={mark} />
	{:else if kind === 'blower'}
		<!-- three blades -->
		{#each [0, 120, 240] as a (a)}
			<path d="M {cx} {cy} q 6 -12 0 -15" fill="none" stroke={mark} stroke-width="2.5" stroke-linecap="round" transform="rotate({a} {cx} {cy})" />
		{/each}
	{:else if kind === 'compressor'}
		<!-- two lines closing towards the outlet -->
		<path d="M {cx - 12} {cy - 15} L {cx + 13} {cy - 7} M {cx - 12} {cy + 15} L {cx + 13} {cy + 7}" stroke={mark} stroke-width="2" />
	{:else}
		<text x={cx} y={cy + 5} text-anchor="middle" font-size="13" font-weight="700" fill={mark}>M</text>
	{/if}
	{#if name}
		<text x={2 * R + 10} y={cy - 3} font-size="12" font-weight="600" fill="var(--ink, #e8e6e1)">{name}</text>
	{/if}
	<text x={2 * R + 10} y={cy + 13} font-size="12" fill={faulted ? 'var(--crit, #e5484d)' : 'var(--ink-2, #c3c2b7)'} font-weight={faulted ? 700 : 400}>{state}</text>
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
		overflow: visible;
	}
</style>
