<script lang="ts">
	// Off-page connector — the ToProcess treatment: an arrowhead where the
	// pipe leaves the diagram, caption alongside. Used here as "FORCE MAIN",
	// where P-101/P-102's discharge (through their check valves) leaves the
	// wet-well mimic. App-supplied via <Mimic registry>, not part of the kit.
	let {
		label = 'FORCE MAIN',
		width = 130
	}: { label?: string; width?: number } = $props();
</script>

<svg viewBox="0 0 130 24" {width} role="img" aria-label={`flow to ${label}`}>
	<path d="M 2 4 L 18 12 L 2 20 Z" fill="var(--axis, #383835)" />
	<text x="24" y="16" font-size="10" font-weight="600" fill="var(--muted, #8a887d)">{label}</text>
</svg>
