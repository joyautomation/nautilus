<script lang="ts" module>
	// A memory module standing in its slot, length along z. Bound to a DIMM UDT.
	import type { KindMeta } from '../defs.js';
	import { partState, partStatus } from './profile.js';
	export const kind: KindMeta = {
		type: 'DIMM',
		members: ['Locator', 'CapacityGB', 'Manufacturer', 'PartNumber', 'Health', 'Fault', 'TempC'],
		bounds: { size: [0.004, 0.031, 0.133], center: [0, 0, 0] },
		status: (v, good) => partStatus('dimm', v, partState({ tag: 'dimm' }, v, good))
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
	let state = $derived(partState({ tag: bound ? 'dimm' : undefined, static: fixed }, value, good));
	let look = $derived(partLook(state, palette, xray));
	const SRC = 'dimm.glb';
	// An empty slot is its socket: a low black bar the length of the DIMM.
	let filler = $derived({ size: [size[0] + 0.002, 0.006, size[2]] as [number, number, number], pos: [0, -size[1] / 2 + 0.003, 0] as [number, number, number] });
</script>

{#if look.fitted}
	<PartModel src={models + SRC} {size} led={look.led} accent={look.accent} opacity={paint?.dim ? 0.15 : look.opacity} tint={paint?.dim ? undefined : paint?.color} />
{:else}
	<EmptySlot {size} wire={paint?.dim ? undefined : (paint?.color ?? look.wire)} faint={paint?.dim} {filler} />
{/if}
