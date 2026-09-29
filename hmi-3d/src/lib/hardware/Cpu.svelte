<script lang="ts" module>
	// The processor under its passive heatsink. Bound to a CPU UDT.
	import type { KindMeta } from '../defs.js';
	import { partState, partStatus } from './profile.js';
	export const kind: KindMeta = {
		type: 'CPU',
		members: ['Model', 'Cores', 'Health', 'Fault', 'TempC', 'Pct'],
		bounds: { size: [0.08, 0.027, 0.108], center: [0, 0, 0] },
		status: (v, good) => partStatus('cpu', v, partState({ tag: 'cpu' }, v, good))
	};
</script>

<script lang="ts">
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { partLook, DEFAULT_MODELS } from './look.js';
	import type { PartProps } from './types.js';
	import PartModel from './PartModel.svelte';
	import EmptySlot from './EmptySlot.svelte';

	let { value, good, size, bound, static: fixed, models = DEFAULT_MODELS, xray = false, paint }: PartProps = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	let state = $derived(partState({ tag: bound ? 'cpu' : undefined, static: fixed }, value, good));
	let look = $derived(partLook(state, palette, xray));
	const SRC = 'cpu.glb';
</script>

{#if look.fitted}
	<PartModel src={models + SRC} {size} led={look.led} accent={look.accent} opacity={paint?.dim ? 0.15 : look.opacity} tint={paint?.dim ? undefined : paint?.color} />
{:else}
	<EmptySlot {size} wire={paint?.dim ? undefined : (paint?.color ?? look.wire)} faint={paint?.dim} />
{/if}
