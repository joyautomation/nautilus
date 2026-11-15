<script lang="ts">
	// An instrument as ISA-5.1 draws it: a bubble with its function letters
	// over its loop number (FIT over 501), and its reading under the bubble.
	// The line across the bubble is the ISA mark for "on the operator's
	// screen". An inline instrument (a flow meter, an analyser in the line)
	// sits ON its line: run the pipe to its `left` and `right` ports, which
	// are the bubble's sides. `top` and `bottom` are there for a tap.
	//
	// Grey, with the reading in the ink colour; `alarm` draws the bubble in
	// the alarm colour; a reading the controller doesn't have reads "—".
	import type { MimicLook } from '../mimic.js';

	let {
		tag = '',
		value,
		unit = '',
		decimals = 1,
		alarm = false,
		present = true,
		label,
		width,
		height,
		look = 'isa101'
	}: {
		/** Its tag number: letters, a dash, the loop number (FIT-501). */
		tag?: string;
		/** The reading. Omit it for an instrument with nothing to show. */
		value?: number | string | null;
		unit?: string;
		decimals?: number;
		/** In alarm: the bubble in the alarm colour. */
		alarm?: boolean;
		/** False when the controller has no reading for it. */
		present?: boolean;
		/** The equipment's label, used as the tag when `tag` is empty. */
		label?: string;
		width?: number;
		height?: number;
		look?: MimicLook;
	} = $props();

	const name = $derived(tag || label || '');
	// FIT-501 -> FIT over 501; a name without a dash shows whole
	const parts = $derived.by(() => {
		const m = /^\s*([A-Za-z]+)[\s-]*([0-9][0-9A-Za-z]*)\s*$/.exec(name);
		return m ? [m[1].toUpperCase(), m[2]] : [name, ''];
	});
	const reading = $derived.by(() => {
		if (value === undefined) return '';
		if (!present || value === null || value === '') return '—';
		const n = typeof value === 'number' ? value : Number(value);
		const v = Number.isFinite(n) ? n.toLocaleString('en-US', { minimumFractionDigits: decimals, maximumFractionDigits: decimals }) : String(value);
		return unit ? `${v} ${unit}` : v;
	});
	const R = 20;
	const w = $derived(width ?? Math.max(2 * R + 4, reading.length * 7.5 + 8));
	const h = $derived(height ?? 2 * R + 4 + (reading ? 18 : 0));
	const cx = $derived(w / 2);
	const cy = R + 2;
	const ink = $derived(alarm ? 'var(--crit, #e5484d)' : 'var(--ink-2, #c3c2b7)');
	const small = $derived(parts[0].length > 3 ? 10 : 12);
	const smallNo = $derived(parts[1].length > 3 ? 10 : 12);
</script>

<svg viewBox="0 0 {w} {h}" width={w} height={h} role="img" aria-label={`${name}${reading ? `: ${reading}` : ''}`}>
	<rect data-part="bubble" x={cx - R} y={cy - R} width={2 * R} height={2 * R} fill="none" />
	<circle {cx} {cy} r={R} fill="var(--surface, #161615)" stroke={ink} stroke-width="2" />
	{#if parts[1]}
		<line x1={cx - R} y1={cy} x2={cx + R} y2={cy} stroke={ink} stroke-width="1.5" />
		<text x={cx} y={cy - 5} text-anchor="middle" font-size={small} font-weight="700" fill="var(--ink, #e8e6e1)">{parts[0]}</text>
		<text x={cx} y={cy + 13} text-anchor="middle" font-size={smallNo} font-weight="600" fill="var(--ink, #e8e6e1)">{parts[1]}</text>
	{:else}
		<text x={cx} y={cy + 4} text-anchor="middle" font-size={small} font-weight="700" fill="var(--ink, #e8e6e1)">{parts[0]}</text>
	{/if}
	{#if reading}
		<text x={cx} y={2 * R + 18} text-anchor="middle" font-size="13" font-weight="600" class="num" fill={reading === '—' ? 'var(--muted, #8d8b85)' : 'var(--ink, #e8e6e1)'}>{reading}</text>
	{/if}
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
		overflow: visible;
	}
	.num {
		font-family: var(--mono, ui-monospace, monospace);
	}
</style>
