<script lang="ts">
	// The controls for whatever was clicked, in a panel on the right of the
	// view. The P&ID (lift-station.mimic.json) and the 3D scene
	// (lift-station.scene.json) give their equipment the same ids, so one
	// panel serves both. Not a dialog: the station keeps running and stays
	// clickable behind it, so you can switch a pump to Hand and watch it
	// start, or pick another piece of equipment. × or Escape closes it.
	import { fly } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import PumpFaceplate from '$lib/PumpFaceplate.svelte';
	import WetWellPanel from '$lib/WetWellPanel.svelte';
	import { num, bool } from '$lib/client.svelte';

	let { selected = $bindable(null) }: { selected?: string | null } = $props();

	type Panel = { title: string; show: 'P101' | 'P102' | 'wetwell' | 'valve' | 'discharge'; pump?: 'P101' | 'P102' };
	// Every clickable id opens something: the floats and the sequence tag
	// belong to the wet well, each check valve to its pump's discharge.
	const PANELS: Record<string, Panel> = {
		P101: { title: 'P-101 submersible pump', show: 'P101' },
		P102: { title: 'P-102 submersible pump', show: 'P102' },
		WW101: { title: 'WW-101 wet well', show: 'wetwell' },
		LSHH101: { title: 'WW-101 wet well', show: 'wetwell' },
		LSLL101: { title: 'WW-101 wet well', show: 'wetwell' },
		SEQ: { title: 'WW-101 wet well', show: 'wetwell' },
		CKV101: { title: 'CKV-101 check valve', show: 'valve', pump: 'P101' },
		CKV102: { title: 'CKV-102 check valve', show: 'valve', pump: 'P102' },
		FIT101: { title: 'Discharge to the force main', show: 'discharge' },
		TOPROC: { title: 'Discharge to the force main', show: 'discharge' }
	};
	const panel = $derived(selected ? PANELS[selected] : undefined);
	const close = () => (selected = null);
	const label = (p: 'P101' | 'P102') => (p === 'P101' ? 'P-101' : 'P-102');
	// It slides in from the right edge and back out, unless the visitor's
	// system asks for less motion.
	const still = typeof matchMedia !== 'undefined' && matchMedia('(prefers-reduced-motion: reduce)').matches;
	const slide = { x: 344, opacity: 1, duration: still ? 0 : 260, easing: cubicOut };
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && close()} />

{#if panel}
	<aside class="panel" aria-label={panel.title} transition:fly={slide}>
		<header>
			<h2>{panel.title}</h2>
			<button class="x" onclick={close} aria-label="Close" title="Close (Esc)">×</button>
		</header>
		<div class="body">
			{#if panel.show === 'P101' || panel.show === 'P102'}
				{#key panel.show}
					<PumpFaceplate pump={panel.show} />
				{/key}
			{:else if panel.show === 'wetwell'}
				<WetWellPanel />
			{:else if panel.show === 'valve' && panel.pump}
				<p class="text">
					A swing check: it stops the force main draining back through {label(panel.pump)}. It is
					mechanical, with no actuator and no signal, so the station cannot command it or read it.
					Pump pressure swings it open while {label(panel.pump)} runs.
				</p>
				<p class="text">
					<b>{bool(`${panel.pump}_Running`) ? `${label(panel.pump)} is running, so it should be open` : `${label(panel.pump)} is stopped, so it should be shut`}</b>
				</p>
				<button class="link" onclick={() => (selected = panel.pump!)}>{label(panel.pump)} controls</button>
			{:else if panel.show === 'discharge'}
				<dl>
					<dt>Flow, FIT-101</dt><dd>{num('FIT101_Flow').toFixed(0)} gpm</dd>
					<dt>Pumped today</dt><dd>{num('StationFlowGal').toLocaleString(undefined, { maximumFractionDigits: 0 })} gal</dd>
					<dt>Pumps running</dt><dd>{Number(bool('P101_Running')) + Number(bool('P102_Running'))} of 2</dd>
				</dl>
			{/if}
		</div>
	</aside>
{/if}

<style>
	.panel {
		position: absolute;
		top: 12px;
		right: 12px;
		max-height: calc(100% - 24px);
		z-index: 5;
		width: min(320px, calc(100% - 24px));
		display: flex;
		flex-direction: column;
		background: color-mix(in srgb, var(--surface) 96%, transparent);
		border: 1px solid var(--border);
		border-radius: var(--radius, 10px);
		box-shadow: 0 8px 28px rgba(0, 0, 0, 0.35);
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		padding: 12px 12px 10px 16px;
		border-bottom: 1px solid var(--border);
	}
	h2 {
		margin: 0;
		font-size: 0.95rem;
		font-weight: 650;
		color: var(--ink);
	}
	.x {
		border: 0;
		background: none;
		color: var(--muted);
		font-size: 1.3rem;
		line-height: 1;
		padding: 2px 6px;
		border-radius: 6px;
		cursor: pointer;
	}
	.x:hover {
		color: var(--ink);
		background: var(--surface-2);
	}
	.body {
		padding: 14px 16px 16px;
		overflow-y: auto;
	}
	.text {
		margin: 0 0 10px;
		font-size: 0.85rem;
		color: var(--ink-2);
	}
	.link {
		border: 1px solid var(--border);
		background: var(--surface-2);
		color: var(--ink);
		border-radius: 6px;
		padding: 6px 10px;
		font: inherit;
		font-size: 0.8rem;
		cursor: pointer;
	}
	dl {
		display: grid;
		grid-template-columns: 1fr auto;
		gap: 8px 10px;
		margin: 0;
		font-size: 0.85rem;
	}
	dt {
		color: var(--ink-2);
	}
	dd {
		margin: 0;
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		color: var(--ink);
		text-align: right;
	}
</style>
