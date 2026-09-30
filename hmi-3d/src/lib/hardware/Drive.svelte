<script lang="ts" module>
	// A drive: a 2.5in carrier in a front bay, or an M.2 stick. Bound to a Drive UDT (Bay, Model, CapacityGB, Health, TempC, Present…).
	import type { KindMeta } from '../defs.js';
	import { partState, partStatus } from './profile.js';
	export const kind: KindMeta = {
		type: 'Drive',
		members: ['Bay', 'Name', 'Model', 'Serial', 'CapacityGB', 'Protocol', 'MediaType', 'Health', 'Fault', 'PredictedFailure', 'TempC', 'Present'],
		bounds: { size: [0.04, 0.038, 0.128], center: [0, 0, 0] },
		status: (v, good) => partStatus('drive', v, partState({ tag: 'drive' }, v, good))
	};
</script>

<script lang="ts">
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { partLook, DEFAULT_MODELS } from './look.js';
	import type { PartProps } from './types.js';
	import PartModel from './PartModel.svelte';
	import EmptySlot from './EmptySlot.svelte';

	let { value, good, size, bound, static: fixed, models = DEFAULT_MODELS, xray = false, paint, form }: PartProps & { form?: 'u2' | 'm2' } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	let state = $derived(partState({ tag: bound ? 'drive' : undefined, static: fixed }, value, good));
	let look = $derived(partLook(state, palette, xray));
	// A front-bay carrier, or an M.2 stick on a card (`form: 'm2'`).
	let SRC = $derived(form === 'm2' ? 'm2.glb' : 'drive.glb');
	// An empty front bay has a blank in it; an empty M.2 socket is just the outline.
	let filler = $derived(form === 'm2' ? undefined : { size: [size[0], size[1], 0.006] as [number, number, number], pos: [0, 0, size[2] / 2 - 0.003] as [number, number, number] });
</script>

{#if look.fitted}
	<PartModel src={models + SRC} {size} led={look.led} accent={look.accent} opacity={paint?.dim ? 0.15 : look.opacity} tint={paint?.dim ? undefined : paint?.color} />
{:else}
	<EmptySlot {size} wire={paint?.dim ? undefined : (paint?.color ?? look.wire)} faint={paint?.dim} {filler} />
{/if}
