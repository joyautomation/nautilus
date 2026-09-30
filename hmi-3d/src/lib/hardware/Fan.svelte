<script lang="ts" module>
	// A fan in the mid-chassis fan wall. Bound to the it-drivers Fan UDT.
	import type { KindMeta } from '../defs.js';
	import { partState, partStatus } from './profile.js';
	export const kind: KindMeta = {
		type: 'Fan',
		members: ['Name', 'Present', 'RPM', 'Pct', 'Fault'],
		bounds: { size: [0.04, 0.04, 0.056], center: [0, 0, 0] },
		status: (v, good) => partStatus('fan', v, partState({ tag: 'fan' }, v, good))
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
	let state = $derived(partState({ tag: bound ? 'fan' : undefined, static: fixed }, value, good));
	let look = $derived(partLook(state, palette, xray));
	const SRC = 'fan.glb';
	// The rotor turns with RPM, slowed ~50x so it reads as motion, not a strobe.
	let spin = $derived.by(() => {
		const r = (value as { RPM?: unknown } | undefined)?.RPM;
		return good && typeof r === 'number' && r > 0 ? Math.min(r / 3000, 4) : 0;
	});
</script>

{#if look.fitted}
	<PartModel src={models + SRC} {size} led={look.led} accent={look.accent} opacity={paint?.dim ? 0.15 : look.opacity} tint={paint?.dim ? undefined : paint?.color} {spin} />
{:else}
	<EmptySlot {size} wire={paint?.dim ? undefined : (paint?.color ?? look.wire)} faint={paint?.dim} />
{/if}
