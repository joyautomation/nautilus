<script lang="ts">
	// A floating alarm sign over whatever is in alarm, so nothing outside the
	// baseline can be missed: the priority's sign (AlarmSign) above the part
	// or device, always facing the viewer, never faded with the rest of a
	// rack, pulsing until the alarm is acknowledged. A device's marker is
	// larger and carries how many alarms it has. An alarm that has returned
	// to normal but is not yet acknowledged is drawn hollow: it happened,
	// it is over, and it still asks to be acknowledged.
	import { HTML } from '@threlte/extras';
	import { getContext } from 'svelte';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { PRIORITY_SIGN } from '../alarms.js';
	import type { Vec3 } from '../scene.js';
	import AlarmSign from './AlarmSign.svelte';

	let {
		at,
		priority,
		unacked,
		active = true,
		count = 1,
		device = false
	}: { at: Vec3; priority: string; unacked: boolean; active?: boolean; count?: number; device?: boolean } = $props();
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	let color = $derived(palette.priority[priority] ?? palette.priority.high);
	let title = $derived(`${count > 1 ? `${count} alarms, worst ` : ''}${priority}: ${PRIORITY_SIGN[priority]?.urgency ?? ''}${active ? '' : ' (returned to normal)'}${unacked ? ' (unacknowledged)' : ''}`);
</script>

<HTML position={at} center pointerEvents="none" zIndexRange={[60, 50]}>
	<span class="mark" class:device class:unacked style:--c={color} title={title}>
		<AlarmSign {priority} {color} size={device ? 30 : 20} hollow={!active} />
		{#if device && count > 1}<b>{count}</b>{/if}
	</span>
</HTML>

<style>
	.mark {
		position: relative;
		display: inline-grid;
		place-items: center;
		filter: drop-shadow(0 1px 2px rgb(0 0 0 / 0.6));
		transform: translateY(-50%);
	}
	/* Unacknowledged: a ring pulses out from the sign (ISA-18.2: an alarm
	   that has not been acknowledged keeps asking). */
	.mark.unacked::after {
		content: '';
		position: absolute;
		inset: -4px;
		border-radius: 50%;
		border: 2px solid var(--c);
		animation: ping 1.4s ease-out infinite;
	}
	b {
		position: absolute;
		right: -8px;
		top: -6px;
		min-width: 16px;
		padding: 0 4px;
		border-radius: 8px;
		font: 700 10px/16px system-ui, sans-serif;
		text-align: center;
		color: #111;
		background: var(--c);
		border: 1px solid #111;
	}
	@keyframes ping {
		from { transform: scale(0.8); opacity: 0.9; }
		to { transform: scale(1.8); opacity: 0; }
	}
	@media (prefers-reduced-motion: reduce) {
		.mark.unacked::after { animation: none; opacity: 0.8; }
	}
</style>
