<script lang="ts">
	// The alarm signs' key while anything is in alarm: a box, or on a phone
	// a chip with the count (Sheet).
	import { AlarmKey, type Palette } from '@joyautomation/nautilus-hmi-3d';
	import Sheet from './Sheet.svelte';

	let { active, palette }: { active: number; palette: Palette } = $props();
</script>

{#if active > 0}
	<Sheet id="alarms" label="What the alarm signs mean" fixed>
		{#snippet chip()}<i class="dot"></i>{active} in alarm{/snippet}
		<div class="key">
			<b>{active} in alarm</b>
			<AlarmKey {palette} />
		</div>
	</Sheet>
{/if}

<style>
	.key {
		display: grid;
		gap: 6px;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--bad, #e5484d);
	}
</style>
