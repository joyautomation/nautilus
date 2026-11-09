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

<!-- Drawn at its width, not scaled to it: a narrower box keeps the arrow
     and the caption their size. -->
<svg viewBox="0 0 {width} 24" {width} height="24" role="img" aria-label={`flow to ${label}`}>
	<path d="M 2 4 L 18 12 L 2 20 Z" fill="var(--edge, #7a7973)" />
	<text x="24" y="16" font-size="10" font-weight="600" fill="var(--muted, #8a887d)">{label}</text>
</svg>
