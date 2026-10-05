<script lang="ts">
	// The inspector's faceplate for a tank: the kit's 2D Tank on the same members.
	import { Tank } from '@joyautomation/nautilus-hmi';
	import { tankState } from '../kinds.js';
	import type { PanelProps } from '../registry.js';
	let { value, label }: PanelProps = $props();
	let s = $derived(tankState(value));
</script>

<Tank levelPct={s.level} tempC={s.tempC} {label} width={200} />
