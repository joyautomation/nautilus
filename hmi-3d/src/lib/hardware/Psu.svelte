<script lang="ts" module>
	// A hot-swap power supply, handle at the rear. Bound to the it-drivers PSU UDT.
	import type { KindMeta } from '../defs.js';
	import { partState, partStatus } from './profile.js';
	export const kind: KindMeta = {
		type: 'PSU',
		members: ['Name', 'Present', 'Fault', 'InputOk', 'InputV', 'OutputW', 'CapacityW'],
		bounds: { size: [0.052, 0.04, 0.22], center: [0, 0, 0] },
		status: (v, good) => partStatus('psu', v, partState({ tag: 'psu' }, v, good))
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
	let state = $derived(partState({ tag: bound ? 'psu' : undefined, static: fixed }, value, good));
	let look = $derived(partLook(state, palette, xray));
	const SRC = 'psu.glb';
</script>

{#if look.fitted}
	<PartModel src={models + SRC} {size} led={look.led} accent={look.accent} opacity={paint?.dim ? 0.15 : look.opacity} tint={paint?.dim ? undefined : paint?.color} />
{:else}
	<EmptySlot {size} wire={paint?.dim ? undefined : (paint?.color ?? look.wire)} faint={paint?.dim} />
{/if}
