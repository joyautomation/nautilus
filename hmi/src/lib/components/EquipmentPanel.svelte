<script lang="ts">
	// The panel a click on a mimic's equipment opens, beside the drawing:
	// its live readings (a number with a sparkline of its last few minutes,
	// a true/false as words), its setpoints, its commands and its switches,
	// all from the equipment's `panel` in the *.mimic.json (MimicPanel). The
	// drawing shows the plant's state and values; this is where an operator
	// changes them. Not a dialog: the plant keeps running and stays
	// clickable behind it. × or Escape closes it.
	//
	//   <Mimic {doc} {tags} {write} onequipmentclick={(eq) => (selected = eq.id)}
	//     clickable={(eq) => !!panelFor(doc, eq.id)} {selected} />
	//   <EquipmentPanel {doc} {tags} {write} bind:selected />
	//
	// Place it in a positioned box (it sits at that box's top right).
	import { fly } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { expandUnits, panelFor, resolveBindings, type MimicDoc, type MimicPanelReading } from '../mimic.js';
	import { sampleTag, tagHistory } from '../taghistory.svelte.js';
	import Sparkline from './Sparkline.svelte';
	import StatusPill from './StatusPill.svelte';
	import SetpointGroup from './SetpointGroup.svelte';
	import CommandButton from './CommandButton.svelte';
	import WriteToggle from './WriteToggle.svelte';

	let {
		doc: source,
		tags = {},
		write,
		selected = $bindable(null)
	}: {
		doc: MimicDoc;
		/** The live tag values, by name (the frame's tags). */
		tags?: Record<string, unknown>;
		write?: (name: string, value: unknown) => Promise<string | null>;
		/** The id of the equipment clicked; null closes the panel. */
		selected?: string | null;
	} = $props();

	const doc = $derived(expandUnits(source));
	const open = $derived(panelFor(doc, selected));
	const close = () => (selected = null);
	const value = (tag: string): unknown => resolveBindings({ v: tag }, tags).v;
	const noWrite = async () => 'This screen is read-only';
	const titleOf = (id: string) => panelFor(doc, id)?.title ?? doc.equipment?.find((e) => e.id === id)?.label ?? id;

	// Every panel's numeric readings keep their history from the moment the
	// page opens, so a panel already has a trend when it's opened.
	$effect(() => {
		const id = setInterval(() => {
			for (const eq of doc.equipment ?? []) {
				if (!eq.panel || typeof eq.panel !== 'object') continue;
				for (const r of eq.panel.readings ?? []) sampleTag(r.tag, value(r.tag));
			}
		}, 1000);
		return () => clearInterval(id);
	});

	const shown = (r: MimicPanelReading): string => {
		const v = value(r.tag);
		if (typeof v === 'number' && isFinite(v)) return v.toFixed(r.decimals ?? 1);
		if (typeof v === 'string' && v) return v;
		return '—';
	};

	const still = typeof matchMedia !== 'undefined' && matchMedia('(prefers-reduced-motion: reduce)').matches;
	const slide = { x: 344, opacity: 1, duration: still ? 0 : 260, easing: cubicOut };
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && open && close()} />

{#if open}
	{@const p = open.panel}
	<aside class="nx-panel" aria-label={open.title} transition:fly={slide}>
		<header>
			<h2>{open.title}</h2>
			<button class="x" onclick={close} aria-label="Close" title="Close (Esc)">×</button>
		</header>
		<div class="body">
			{#if p.text}<p class="text">{p.text}</p>{/if}
			{#if p.readings?.length}
				<dl class="readings">
					{#each p.readings as r, i (i)}
						{@const v = value(r.tag)}
						<dt>{r.label ?? r.tag}</dt>
						<dd>
							{#if typeof v === 'boolean'}
								<StatusPill
									kind={v ? (r.alarm ? 'critical' : 'good') : 'off'}
									label={v ? (r.on ?? (r.alarm ? 'Alarm' : 'On')) : (r.off ?? (r.alarm ? 'Normal' : 'Off'))}
								/>
							{:else}
								<span class="num">{shown(r)}</span>{#if r.unit}<span class="unit">{r.unit}</span>{/if}
								{#if typeof v === 'number'}
									<span class="spark" aria-hidden="true">
										<Sparkline values={tagHistory(r.tag)} height={18} yMin={r.min} yMax={r.max} color="var(--ink-2, #c3c2b7)" compact />
									</span>
								{/if}
							{/if}
						</dd>
					{/each}
				</dl>
			{/if}
			{#if p.commands?.length}
				<div class="commands">
					{#each p.commands as c, i (i)}
						<CommandButton
							tag={c.tag}
							label={c.label}
							kind={c.kind ?? 'neutral'}
							pulseMs={c.pulseMs ?? 400}
							held={c.held ? value(c.held) === true : false}
							write={write ?? noWrite}
						/>
					{/each}
				</div>
			{/if}
			{#if p.toggles?.length}
				<div class="toggles">
					{#each p.toggles as t, i (i)}
						<WriteToggle
							tag={t.tag}
							label={t.label}
							value={value(t.tag) === true}
							present={t.tag in tags}
							invert={t.invert ?? false}
							write={write ?? noWrite}
						/>
					{/each}
				</div>
			{/if}
			{#if p.setpoints?.length}
				<SetpointGroup title="Setpoints" items={p.setpoints} {tags} {write} />
			{/if}
			{#if p.links?.length}
				<div class="links">
					{#each p.links as id, i (i)}
						<button class="link" onclick={() => (selected = id)}>{titleOf(id)}</button>
					{/each}
				</div>
			{/if}
		</div>
	</aside>
{/if}

<style>
	.nx-panel {
		position: absolute;
		top: 12px;
		right: 12px;
		max-height: calc(100% - 24px);
		z-index: 5;
		width: min(320px, calc(100% - 24px));
		box-sizing: border-box;
		display: flex;
		flex-direction: column;
		background: color-mix(in srgb, var(--surface, #161615) 96%, transparent);
		border: 1px solid var(--border, #2a2a28);
		border-radius: var(--radius, 10px);
		box-shadow: 0 8px 28px rgba(0, 0, 0, 0.35);
		color: var(--ink, #e6e6e6);
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		padding: 12px 12px 10px 16px;
		border-bottom: 1px solid var(--border, #2a2a28);
	}
	h2 {
		margin: 0;
		font-size: 0.95rem;
		font-weight: 650;
	}
	.x {
		border: 0;
		background: none;
		color: var(--muted, #8b949e);
		font-size: 1.3rem;
		line-height: 1;
		padding: 2px 6px;
		border-radius: 6px;
		cursor: pointer;
	}
	.x:hover {
		color: var(--ink, #e6e6e6);
		background: var(--surface-2, #232321);
	}
	.body {
		padding: 14px 16px 16px;
		overflow-y: auto;
		display: grid;
		gap: 14px;
	}
	.text {
		margin: 0;
		font-size: 0.85rem;
		color: var(--ink-2, #c3c2b7);
	}
	.readings {
		display: grid;
		grid-template-columns: 1fr auto;
		align-items: center;
		gap: 8px 10px;
		margin: 0;
		font-size: 0.85rem;
	}
	dt {
		color: var(--ink-2, #c3c2b7);
	}
	dd {
		margin: 0;
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.3em;
	}
	.num {
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-weight: 650;
	}
	.unit {
		color: var(--muted, #8b949e);
	}
	.spark {
		display: block;
		width: 56px;
		margin-left: 0.4em;
	}
	.commands,
	.links {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
	}
	.toggles {
		display: grid;
		gap: 8px;
	}
	.link {
		border: 1px solid var(--border, #2a2a28);
		background: var(--surface-2, #232321);
		color: var(--ink, #e6e6e6);
		border-radius: 6px;
		padding: 6px 10px;
		font: inherit;
		font-size: 0.8rem;
		cursor: pointer;
	}
</style>
