<script lang="ts">
	// Radial gauge: 240° arc, optional setpoint marker.
	let {
		value = 0,
		min = 0,
		max = 100,
		unit = '',
		label = '',
		color = 'var(--s1)',
		setpoint,
		decimals = 1,
		width = 170
	}: {
		value?: number;
		min?: number;
		max?: number;
		unit?: string;
		label?: string;
		color?: string;
		setpoint?: number;
		decimals?: number;
		width?: number;
	} = $props();

	const CX = 85, CY = 82, R = 60;

	function polar(frac: number): [number, number] {
		const a = ((-120 + 240 * frac) * Math.PI) / 180;
		return [CX + R * Math.sin(a), CY - R * Math.cos(a)];
	}

	function arc(f0: number, f1: number) {
		const [x0, y0] = polar(f0);
		const [x1, y1] = polar(f1);
		// large-arc flips at 180° of actual sweep; the full scale spans 240°.
		const large = (f1 - f0) * 240 > 180 ? 1 : 0;
		return `M ${x0} ${y0} A ${R} ${R} 0 ${large} 1 ${x1} ${y1}`;
	}

	let frac = $derived(Math.max(0, Math.min(1, (value - min) / (max - min || 1))));
	let spFrac = $derived(
		setpoint === undefined ? null : Math.max(0, Math.min(1, (setpoint - min) / (max - min || 1)))
	);
</script>

<svg viewBox="0 0 170 130" {width} role="img" aria-label={`${label}: ${value.toFixed(decimals)} ${unit}`}>
	<path d={arc(0, 1)} fill="none" stroke="var(--grid)" stroke-width="10" stroke-linecap="round" />
	{#if frac > 0.005}
		<path class="val" d={arc(0, frac)} fill="none" stroke={color} stroke-width="10" stroke-linecap="round" />
	{/if}
	{#if spFrac !== null}
		{@const [sx, sy] = polar(spFrac)}
		<circle class="sp" cx={sx} cy={sy} r="5" fill="var(--surface)" stroke="var(--s2)" stroke-width="2.5">
			<title>Setpoint: {setpoint}</title>
		</circle>
	{/if}
	<text x={CX} y={CY + 2} text-anchor="middle" font-size="26" font-weight="650" fill="var(--ink)" class="num">
		{value.toFixed(decimals)}
	</text>
	<text x={CX} y={CY + 18} text-anchor="middle" font-size="11" fill="var(--muted)">{unit}</text>
	<text x={CX} y={CY + 42} text-anchor="middle" font-size="12" font-weight="600" fill="var(--ink-2)">{label}</text>
	{#each [0, 1] as f}
		{@const [tx, ty] = polar(f)}
		<text x={tx} y={ty + 16} text-anchor="middle" font-size="10" fill="var(--muted)" class="num">{f ? max : min}</text>
	{/each}
</svg>

<style>
	/* Smooth updates. `d` interpolates in Chromium when the path structure is
	   unchanged; elsewhere it falls back to stepping. */
	.val {
		transition: d 0.18s linear;
	}
	.sp {
		transition:
			cx 0.18s linear,
			cy 0.18s linear;
	}
</style>
