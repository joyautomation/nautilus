<script lang="ts">
	// Off-page connector — the mini-scada TO PROCESS treatment: an arrowhead
	// where the pipe leaves the diagram, caption alongside. App-supplied via
	// <Mimic registry>, not part of the kit.
	let {
		label = 'TO PROCESS',
		width = 120
	}: { label?: string; width?: number } = $props();
</script>

<svg viewBox="0 0 120 24" {width} role="img" aria-label={`flow ${label}`}>
	<path d="M 2 4 L 18 12 L 2 20 Z" fill="var(--axis, #383835)" />
	<text x="24" y="16" font-size="10" fill="var(--muted, #8a887d)">{label}</text>
</svg>
