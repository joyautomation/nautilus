<script lang="ts">
	import { Pump } from '@joyautomation/nautilus-hmi';
	import { pumpState } from '../kinds.js';
	import type { PanelProps } from '../registry.js';
	let { value, label }: PanelProps = $props();
	let s = $derived(pumpState(value));
</script>

<Pump running={s.running} speedPct={s.speed} {label} />
