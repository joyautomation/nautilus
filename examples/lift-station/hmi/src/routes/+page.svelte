<script lang="ts">
	// The P&ID: the station's mimic (lift-station.mimic.json) filling the
	// window, live. Click a pump, the wet well, a float, a check valve or the
	// discharge for its controls (EquipmentPanel). The same ids, and the same
	// panel, serve the 3D view. Every value comes from a tag in /api/stream;
	// nothing here invents a value the controller doesn't publish.
	import { Mimic, type MimicDoc } from '@joyautomation/nautilus-hmi';
	// lift-station.mimic.json lives at the nautilus PROJECT root: see
	// hmi/vite.config.ts's server.fs.allow and the README for why.
	import mimicDoc from '../../../lift-station.mimic.json';
	import SubmersiblePump from '$lib/SubmersiblePump.svelte';
	import FloatSwitch from '$lib/FloatSwitch.svelte';
	import ToProcess from '$lib/ToProcess.svelte';
	import SeqStepTag from '$lib/SeqStepTag.svelte';
	import EquipmentPanel from '$lib/EquipmentPanel.svelte';
	import { rt } from '$lib/client.svelte';

	const doc = mimicDoc as unknown as MimicDoc;
	const tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	let selected = $state<string | null>(null);
</script>

<svelte:head><title>Lift Station · P&ID</title></svelte:head>

{#if !rt.frame}
	<div class="waiting">
		<p>Connecting to the controller…</p>
		<p class="hint">
			Run <code>naut run .</code> in <code>examples/lift-station</code>, then this page follows its
			<code>/api/stream</code>.
		</p>
	</div>
{:else}
	<!-- With a panel open, the drawing moves over to make room for it. -->
	<div class="stage" class:with-panel={selected !== null} style:--ratio={doc.canvas.width / doc.canvas.height}>
		<div class="fit">
			<Mimic {doc} {tags} registry={{ SubmersiblePump, FloatSwitch, ToProcess, SeqStepTag }} onequipmentclick={(eq) => (selected = eq.id)} />
		</div>
		<p class="tip">Click any equipment for its controls.</p>
	</div>
	<EquipmentPanel bind:selected />
{/if}

<style>
	/* The drawing as large as the window allows, whole: as wide as it can
	   be, unless that would make it taller than the space it has. */
	.stage {
		position: absolute;
		inset: 0;
		padding: 16px 24px 36px;
		display: grid;
		place-items: center;
		container-type: size;
	}
	.stage.with-panel {
		padding-right: 356px;
	}
	/* The drawing eases over as the panel slides in, and back as it goes. */
	@media (prefers-reduced-motion: no-preference) {
		.stage {
			transition: padding-right 260ms cubic-bezier(0.33, 1, 0.68, 1);
		}
	}
	.fit {
		width: min(100cqw, 100cqh * var(--ratio));
	}
	@media (max-width: 900px) {
		.stage.with-panel {
			padding-right: 24px;
		}
	}
	.tip {
		position: absolute;
		bottom: 10px;
		left: 0;
		right: 0;
		margin: 0;
		text-align: center;
		font-size: 0.75rem;
		color: var(--muted);
	}
	.waiting {
		padding: 4rem 1rem;
		text-align: center;
		color: var(--muted);
	}
	.waiting .hint {
		font-size: 0.85rem;
	}
	code {
		font-family: var(--mono);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 4px;
		padding: 0.05rem 0.3rem;
		color: var(--accent, var(--s1));
	}
</style>
