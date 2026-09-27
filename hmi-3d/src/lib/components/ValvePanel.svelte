<script lang="ts">
	import { Valve } from '@joyautomation/nautilus-hmi';
	import { valveState } from '../kinds.js';
	import type { PanelProps } from '../registry.js';
	let { value, label }: PanelProps = $props();
	let s = $derived(valveState(value));
</script>

<Valve openPct={s.pos} {label} />
