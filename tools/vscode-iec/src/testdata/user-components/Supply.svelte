<script lang="ts">
	// Battery-limit source box — the mini-scada SUPPLY treatment: a bordered
	// tag with the header temperature under the name. App-supplied via
	// <Mimic registry>, not part of the kit.
	let {
		tempC,
		label = 'SUPPLY',
		width = 80
	}: { tempC?: number; label?: string; width?: number } = $props();
</script>

<svg viewBox="0 0 80 50" {width} role="img" aria-label={`${label} header`}>
	<rect x="1" y="1" width="78" height="48" rx="8" fill="var(--surface-2, #232321)" stroke="var(--axis, #383835)" stroke-width="2" />
	<text x="40" y="21" text-anchor="middle" font-size="10" font-weight="600" fill="var(--ink-2, #c3c2b7)">{label}</text>
	<text x="40" y="37" text-anchor="middle" font-size="10" fill="var(--muted, #8a887d)" class="num">
		{typeof tempC === 'number' && isFinite(tempC) ? `${tempC.toFixed(0)} °C` : '—'}
	</text>
</svg>
