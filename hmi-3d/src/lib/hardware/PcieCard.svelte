<script lang="ts" module>
	// An add-in card lying flat on its riser, bracket at the rear. Bound to a PCIeDevice UDT; Ports sets how many cages show.
	import type { KindMeta } from '../defs.js';
	import { partState, partStatus } from './profile.js';
	export const kind: KindMeta = {
		type: 'PCIeDevice',
		members: ['Slot', 'Name', 'Model', 'Manufacturer', 'Firmware', 'Health', 'Fault', 'Ports', 'TempC'],
		bounds: { size: [0.069, 0.012, 0.168], center: [0, 0, 0] },
		status: (v, good) => partStatus('pcie-card', v, partState({ tag: 'pcie-card' }, v, good))
	};
</script>

<script lang="ts">
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { partLook, DEFAULT_MODELS } from './look.js';
	import type { PartProps } from './types.js';
	import PartModel from './PartModel.svelte';
	import EmptySlot from './EmptySlot.svelte';

	let { value, good, size, bound, static: fixed, models = DEFAULT_MODELS, xray = false }: PartProps = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	let state = $derived(partState({ tag: bound ? 'pcie-card' : undefined, static: fixed }, value, good));
	let look = $derived(partLook(state, palette, xray));
	const SRC = 'card.glb';
	// The fitted card's port count shows as its cages; the profile's stand-in
	// value supplies it for a card no driver reports.
	let ports = $derived.by(() => {
		const v = (value ?? fixed) as { Ports?: unknown } | undefined;
		return typeof v?.Ports === 'number' ? v.Ports : undefined;
	});
</script>

{#if look.fitted}
	<PartModel src={models + SRC} {size} led={look.led} accent={look.accent} opacity={look.opacity} {ports} />
{:else}
	<EmptySlot {size} wire={look.wire} />
{/if}
