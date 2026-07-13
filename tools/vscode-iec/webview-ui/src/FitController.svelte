<script lang="ts">
	// Fits the viewport when the diagram's STRUCTURE changes (new/removed
	// nodes), leaving pan/zoom alone for value-only re-renders. Must live
	// inside <SvelteFlow> for flow context.
	import { useSvelteFlow } from '@xyflow/svelte';

	let { structureKey }: { structureKey: string } = $props();
	const { fitView } = useSvelteFlow();
	let last = '';

	$effect(() => {
		if (structureKey !== last) {
			last = structureKey;
			// Wait a tick so freshly-measured nodes have dimensions.
			requestAnimationFrame(() => void fitView({ padding: 0.1 }));
		}
	});
</script>
