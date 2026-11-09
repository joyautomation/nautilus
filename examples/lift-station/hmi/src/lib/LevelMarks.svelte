<script lang="ts">
	// Where the level setpoints are, against the wet well's scale: an arrow
	// at each one's level pointing at the scale, and its name and value.
	// Placed in the mimic beside WW-101's scale, its height the tank's
	// inside plus `pad` above and below, so 0-100 % here lines up with the
	// scale's 0-100. Labels too close to read are nudged apart; their arrows
	// stay at the true level. App-supplied via <Mimic registry>, not part of
	// the kit.
	let {
		leadOn,
		lagOn,
		lagOff,
		leadOff,
		levelSP,
		highAlarm,
		width = 130,
		height = 234,
		pad = 6
	}: {
		leadOn?: number;
		lagOn?: number;
		lagOff?: number;
		leadOff?: number;
		levelSP?: number;
		highAlarm?: number;
		width?: number;
		height?: number;
		pad?: number;
	} = $props();

	const GAP = 10; // the least room between two labels
	const span = $derived(height - 2 * pad);
	const at = (pct: number) => pad + (1 - Math.max(0, Math.min(100, pct)) / 100) * span;

	type Mark = { name: string; pct?: number; alarm?: boolean; loop?: boolean };
	const marks = $derived.by(() => {
		const all: Mark[] = [
			{ name: 'High alarm', pct: highAlarm, alarm: true },
			{ name: 'Lag on', pct: lagOn },
			{ name: 'Lead on', pct: leadOn },
			{ name: 'Lag off', pct: lagOff },
			{ name: 'Level SP', pct: levelSP, loop: true },
			{ name: 'Lead off', pct: leadOff }
		];
		const placed = all
			.flatMap((m) => (typeof m.pct === 'number' && isFinite(m.pct) ? [{ ...m, pct: m.pct, y: at(m.pct), ly: at(m.pct) }] : []))
			.sort((a, b) => a.y - b.y);
		for (let i = 1; i < placed.length; i++) placed[i].ly = Math.max(placed[i].ly, placed[i - 1].ly + GAP);
		return placed;
	});
</script>

<svg viewBox="0 0 {width} {height}" {width} {height} role="img" aria-label="Level setpoints">
	{#each marks as m (m.name)}
		{@const c = m.alarm ? 'var(--serious, #ec835a)' : m.loop ? 'var(--s1, #3794ff)' : 'var(--ink-2, #c3c2b7)'}
		<!-- the arrow at the true level, pointing at the scale -->
		<path d="M 0 {m.y} L 7 {m.y - 3.5} L 7 {m.y + 3.5} Z" fill={c} />
		{#if m.ly !== m.y}
			<line x1="7" y1={m.y} x2="11" y2={m.ly} stroke={c} stroke-width="0.75" />
		{/if}
		<text x="13" y={m.ly + 3} font-size="8" fill={c}>
			{m.name} <tspan font-weight="650">{m.pct.toFixed(0)}</tspan>
		</text>
	{/each}
</svg>

<style>
	svg {
		max-width: 100%;
		height: auto;
		overflow: visible;
	}
</style>
