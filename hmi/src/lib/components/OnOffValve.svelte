<script lang="ts">
	// An on/off valve as an ISA-101 screen draws one: the bowtie with its
	// actuator, its tag number beside it, and its state at a glance. Open is
	// the body filled; closed is the outline; travelling is one half filled
	// (the valve between its limits); failed is drawn in the alarm colour
	// with the word. Grey otherwise: colour is for the abnormal.
	//
	// `vertical` turns it for a drop (a line running down), with the tag to
	// its right. Pipes meet the body on any side (ports `left`, `right`,
	// `top`, `bottom`): a horizontal valve's ends are left and right, a
	// vertical one's top and bottom.
	import type { MimicLook } from '../mimic.js';

	let {
		tag = '',
		open = false,
		travelling = false,
		failed = false,
		vertical = false,
		label,
		width,
		height,
		look = 'isa101'
	}: {
		/** Its tag number, XV-101. */
		tag?: string;
		/** Open feedback. */
		open?: boolean;
		/** Between its limits: commanded one way, not there yet. */
		travelling?: boolean;
		/** Didn't reach its position in time (a fail alarm). */
		failed?: boolean;
		/** Turned for a vertical line. */
		vertical?: boolean;
		/** The equipment's label, used as the tag when `tag` is empty. */
		label?: string;
		width?: number;
		height?: number;
		look?: MimicLook;
	} = $props();

	const name = $derived(tag || label || '');
	// the body is 28 along the line and 20 across, an actuator above it
	const L = 28, A = 20, ACT = 14;
	const textW = $derived(Math.max(name.length, failed ? 4 : 0) * 7.5 + 8);
	const w = $derived(width ?? (vertical ? ACT + A + 6 + textW : Math.max(L, textW)));
	const h = $derived(height ?? (vertical ? Math.max(L + 4, 40) : ACT + A + 4 + 16 + (failed ? 19 : 0)));
	// body box: along the line `L`, across `A`
	const bx = $derived(vertical ? ACT : (w - L) / 2);
	const by = $derived(vertical ? (h - L) / 2 : ACT);
	const bw = $derived(vertical ? A : L);
	const bh = $derived(vertical ? L : A);
	const cx = $derived(bx + bw / 2);
	const cy = $derived(by + bh / 2);
	const left = $derived(vertical ? `M ${bx} ${by} L ${bx + bw} ${by} L ${cx} ${cy} Z` : `M ${bx} ${by} L ${bx} ${by + bh} L ${cx} ${cy} Z`);
	const right = $derived(vertical ? `M ${bx} ${by + bh} L ${bx + bw} ${by + bh} L ${cx} ${cy} Z` : `M ${bx + bw} ${by} L ${bx + bw} ${by + bh} L ${cx} ${cy} Z`);
	const ink = $derived(failed ? 'var(--crit, #e5484d)' : 'var(--ink-2, #c3c2b7)');
	const fillA = $derived(open || travelling ? ink : 'var(--bg, #0d0d0d)');
	const fillB = $derived(open ? ink : 'var(--bg, #0d0d0d)');
	const state = $derived(failed ? 'failed' : travelling ? 'travelling' : open ? 'open' : 'closed');
</script>

<svg viewBox="0 0 {w} {h}" width={w} height={h} role="img" aria-label={`Valve ${name}: ${state}`} class:failed>
	<rect data-part="body" x={bx} y={by} width={bw} height={bh} fill="none" />
	<path d={left} fill={fillA} stroke={ink} stroke-width="2" stroke-linejoin="round" />
	<path d={right} fill={fillB} stroke={ink} stroke-width="2" stroke-linejoin="round" />
	<!-- the actuator: a stem and a dome, up for a horizontal valve, left for a vertical one -->
	{#if vertical}
		<line x1={cx} y1={cy} x2={11} y2={cy} stroke={ink} stroke-width="2" />
		<path d="M 11 {cy - 8} A 8 8 0 0 0 11 {cy + 8} Z" fill="var(--surface-2, #232321)" stroke={ink} stroke-width="2" />
	{:else}
		<line x1={cx} y1={cy} x2={cx} y2={12} stroke={ink} stroke-width="2" />
		<path d="M {cx - 9} 12 A 9 9 0 0 1 {cx + 9} 12 Z" fill="var(--surface-2, #232321)" stroke={ink} stroke-width="2" />
	{/if}
	{#if name}
		<text
			x={vertical ? bx + bw + 6 : w / 2}
			y={vertical ? cy - (failed ? 2 : -4) : by + bh + 15}
			text-anchor={vertical ? 'start' : 'middle'}
			font-size="12"
			font-weight="600"
			fill="var(--ink, #e8e6e1)">{name}</text
		>
	{/if}
	{#if failed}
		{@const fx = vertical ? bx + bw + 6 : w / 2 - 17}
		{@const fy = vertical ? cy + 2 : by + bh + 19}
		<rect x={fx} y={fy} width="34" height="15" rx="3" fill="none" stroke="var(--crit, #e5484d)" stroke-width="1.5" />
		<text x={fx + 17} y={fy + 11.5} text-anchor="middle" font-size="11" font-weight="700" fill="var(--ink, #e8e6e1)">FAIL</text>
	{/if}
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
		overflow: visible;
	}
	path {
		transition: fill 0.25s linear;
	}
</style>
