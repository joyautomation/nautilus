<script lang="ts">
	// Fits the viewport when the diagram's STRUCTURE changes (new/removed
	// nodes), leaving pan/zoom alone for value-only re-renders. Must live
	// inside <SvelteFlow> for flow context. Also the FBD half of the zoom
	// keys Ladder/SFC get from ZoomPane: Ctrl+= / Ctrl+- / Ctrl+0 (fit).
	import { useSvelteFlow } from '@xyflow/svelte';

	let { structureKey }: { structureKey: string } = $props();
	const { fitView, zoomIn, zoomOut } = useSvelteFlow();
	let last = '';

	$effect(() => {
		if (structureKey !== last) {
			last = structureKey;
			// Wait a tick so freshly-measured nodes have dimensions.
			requestAnimationFrame(() => void fitView({ padding: 0.1 }));
		}
	});

	function onkeydown(ev: KeyboardEvent) {
		if (!(ev.ctrlKey || ev.metaKey) || ev.altKey) return;
		// The canvas has focus (a node, the pane) — or nothing does; never
		// while typing in a field or a panel outside the flow.
		const ae = document.activeElement;
		if (ae && ae !== document.body && !ae.closest('.svelte-flow')) return;
		if (ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA')) return;
		let acted = true;
		if (ev.key === '=' || ev.key === '+' || ev.code === 'NumpadAdd') void zoomIn();
		else if (ev.key === '-' || ev.key === '_' || ev.code === 'NumpadSubtract') void zoomOut();
		else if (ev.key === '0' || ev.code === 'Numpad0') void fitView({ padding: 0.1 });
		else acted = false;
		if (acted) {
			// Keep VS Code's window zoom out of it.
			ev.preventDefault();
			ev.stopPropagation();
		}
	}
</script>

<!-- capture: ahead of any bubble-phase listener on window (VS Code's
     keybinding forwarder included) -->
<svelte:window onkeydowncapture={onkeydown} />
