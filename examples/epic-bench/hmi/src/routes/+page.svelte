<script lang="ts">
	// The bench groov EPIC in 3D, lit from its live tags. The model is plain
	// Threlte (src/lib/Epic.svelte) placed inside the kit's SceneView, which
	// brings the canvas, the orbit camera and the scene context the model
	// reads its tags from. Read-only: this page writes nothing.
	import { onMount } from 'svelte';
	import { RealtimeClient, ConnectionBadge, type NautilusFrame } from '@joyautomation/nautilus-hmi';
	import { SceneView } from '@joyautomation/nautilus-hmi-3d';
	import Epic, { BOUNDS, type Channel } from '$lib/Epic.svelte';
	import Panel, { type Row } from '$lib/Panel.svelte';
	import { fitCamera } from '$lib/fit';

	const rt = new RealtimeClient<NautilusFrame>({ url: '/api/stream' });

	// The window's shape decides where the panel goes, and the stage's shape
	// (what is left for the canvas) decides how far back the camera sits.
	let innerWidth = $state(1920);
	let innerHeight = $state(1080);
	let wide = $derived(innerWidth / innerHeight > 1.3);
	let stage = $state<HTMLElement>();
	let aspect = $state(1.5);
	let camera = $derived(fitCamera(BOUNDS, aspect, { yaw: 20, pitch: 9, fov: 26, fill: 0.96 }));

	let hover = $state<Channel | null>(null);
	let pointer = $state({ x: 0, y: 0 });

	let tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	let scan = $derived(rt.frame?.scan);
	let driver = $derived(rt.frame?.drivers?.[0]);
	const n = (tag: string, digits: number) => {
		const v = tags[tag];
		return typeof v === 'number' && rt.isGood(tag) ? v.toFixed(digits) : '—';
	};
	const count = (tag: string) => (typeof tags[tag] === 'number' ? String(tags[tag]) : '—');
	const ms = (v: number | undefined, digits: number) => (typeof v === 'number' ? v.toFixed(digits) : '—');

	let rows = $derived<Row[]>([
		{ label: 'Driver', value: driver ? `${driver.kind} ${driver.state}` : '—', tone: driver && driver.state !== 'connected' ? 'crit' : undefined },
		{ label: 'I/O online', value: tags.EPIC__Online === true ? 'yes' : tags.EPIC__Online === false ? 'no' : '—', tone: tags.EPIC__Online === false ? 'crit' : undefined },
		{ label: 'Outages', value: count('EPIC__Outages') },
		{ label: 'Watchdog trips', value: count('EPIC__WatchdogTrips') },
		{ label: 'Outputs held', value: tags.BenchHeld === true ? 'yes' : tags.BenchHeld === false ? 'no' : '—', tone: tags.BenchHeld === true ? 'warn' : undefined },
		{ label: 'Scan period', value: ms(scan?.periodMs, 1), unit: 'ms' },
		{ label: 'Scan time', value: ms(scan?.lastMs, 2), unit: 'ms' },
		{ label: 'Heartbeat', value: n('BlinkPeriodS', 1), unit: 's', lamp: tags.Blink === true },
		{ label: 'Bench temperature', value: n('BenchTempC', 1), unit: '°C' },
		{ label: 'Bench voltage', value: n('BenchVoltsV', 2), unit: 'V' }
	]);

	onMount(() => {
		rt.start();
		const watch = new ResizeObserver(([e]) => {
			const { width, height } = e.contentRect;
			if (width > 0 && height > 0) aspect = width / height;
		});
		if (stage) watch.observe(stage);
		return () => {
			watch.disconnect();
			rt.stop();
		};
	});
</script>

<svelte:window bind:innerWidth bind:innerHeight />

<div class="screen" class:wide>
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="stage" bind:this={stage} onpointermove={(e) => (pointer = { x: e.clientX, y: e.clientY })}>
		<SceneView {rt} {camera} inspector={false}>
			<Epic hovered={hover?.tag ?? null} onhover={(ch) => (hover = ch)} />
			{#snippet hud()}
				<ConnectionBadge state={rt.connected ? 'connected' : 'offline'} label="epic-bench" size="sm" />
			{/snippet}
		</SceneView>
		{#if hover}
			<div class="tip" style:left="{pointer.x + 14}px" style:top="{pointer.y + 14}px"><code>{hover.tag}</code>{hover.text()}</div>
		{/if}
	</div>
	<Panel name="groov EPIC" model="Opto 22 GRV-EPIC-PR1" {rows} {wide} />
</div>

<style>
	.screen {
		position: fixed;
		inset: 0;
		display: grid;
		grid-template-rows: minmax(0, 1fr) auto;
	}
	.screen.wide {
		grid-template-rows: none;
		grid-template-columns: minmax(0, 1fr) clamp(280px, 23vw, 460px);
	}
	.stage {
		position: relative;
		min-width: 0;
		min-height: 0;
		background: radial-gradient(ellipse at 50% 45%, #2a2a28 0%, var(--surface, #1a1a19) 75%);
	}
	.tip {
		position: fixed;
		z-index: 2;
		pointer-events: none;
		padding: 6px 10px;
		border-radius: 6px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 92%, transparent);
		font: 14px/1.2 system-ui, sans-serif;
		white-space: nowrap;
	}
	.tip code {
		font-family: ui-monospace, monospace;
		margin-right: 10px;
		color: var(--ink-2, #c3c2b7);
	}
</style>
