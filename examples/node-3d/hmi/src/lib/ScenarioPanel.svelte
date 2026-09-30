<script lang="ts">
	// The plant's scenarios and the faults that are set, beside the 3D.
	// Only when the plant answers (a live controller has none), and marked
	// SIMULATION: every button here writes the plant's fault inputs, never a
	// device. A scenario ADDS its faults to what is set; clear all is
	// Scenario := normal. Right-click a part in the 3D for its own faults.
	import { activeFaults, since } from './faults';
	import type { PlantFaults } from './faults.svelte';

	let { faults }: { faults: PlantFaults } = $props();
	let open = $state(true);
	let active = $derived(activeFaults(faults.state));
	let scenarios = $derived(faults.catalog.filter((s) => s.name !== 'normal'));
	let running = $derived(faults.scenario && faults.scenario !== 'normal' ? faults.scenario : '');
</script>

{#if faults.up}
	<section class="panel" aria-label="Scenarios (simulation)">
		<header>
			<span class="sim">SIMULATION</span>
			<b>Scenarios</b>
			<button class="toggle" aria-expanded={open} onclick={() => (open = !open)}>{open ? '▾' : '▸'}</button>
		</header>
		{#if open}
			<div class="now">
				{#if running}
					<span class="dot"></span>
					<span><b>{running}</b> <span class="muted">for {since(faults.since)}</span></span>
				{:else}
					<span class="muted">The recording as it was{active.length ? ', plus faults set by hand' : ''}.</span>
				{/if}
				{#if faults.unknown}<span class="bad">no scenario by that name</span>{/if}
			</div>

			<h3>
				Active faults <em>{active.length}</em>
				<button class="clear" disabled={!active.length && !running} onclick={faults.clearAll}>clear all</button>
			</h3>
			{#if active.length}
				<ul class="active">
					{#each active as f (f.tag + f.member)}
						<li>
							<span><b>{f.who}</b> {f.label}{#if typeof f.value === 'number' && f.value !== f.set}<span class="muted"> ({f.value})</span>{/if}</span>
							<button aria-label="Clear {f.who} {f.label}" title="Clear" onclick={() => faults.write(`${f.tag}.${f.member}`, f.clear)}>×</button>
						</li>
					{/each}
				</ul>
			{:else}
				<p class="muted">None. Run a scenario, or right-click a part.</p>
			{/if}

			<h3>Run <span class="muted">adds to what is set</span></h3>
			<ul class="list">
				{#each scenarios as s (s.name)}
					<li>
						<button class:on={running === s.name} onclick={() => faults.run(s.name)}>
							<b>{s.name}</b>
							<span>{s.about}</span>
						</button>
					</li>
				{/each}
			</ul>
		{/if}
	</section>
{/if}

<style>
	.panel {
		width: min(320px, calc(100vw - 24px));
		max-height: 100%;
		min-height: 0;
		flex: 0 1 auto;
		display: flex;
		flex-direction: column;
		border-radius: 8px;
		border: 1px solid var(--axis, #383835);
		background: color-mix(in srgb, var(--surface, #1a1a19) 92%, transparent);
		color: var(--ink, #e8e6e1);
		font: 12px/1.35 system-ui, sans-serif;
		overflow: hidden;
		pointer-events: auto;
	}
	header {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 10px;
		border-bottom: 1px solid var(--axis, #383835);
	}
	header b {
		flex: 1;
	}
	.sim {
		font: 600 10px/1 system-ui, sans-serif;
		letter-spacing: 0.08em;
		padding: 3px 6px;
		border-radius: 4px;
		border: 1px solid var(--warn, #d9a441);
		color: var(--warn, #d9a441);
	}
	.toggle {
		border: 0;
		background: transparent;
		color: var(--ink-2, #a8a6a1);
		cursor: pointer;
		font-size: 12px;
	}
	.now {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
		padding: 8px 10px 0;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--warn, #d9a441);
	}
	.bad {
		color: var(--bad, #e5484d);
	}
	.muted {
		color: var(--ink-2, #a8a6a1);
		font-weight: 400;
	}
	h3 {
		display: flex;
		align-items: baseline;
		gap: 6px;
		margin: 10px 10px 4px;
		font: 600 11px/1.2 system-ui, sans-serif;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--ink-2, #a8a6a1);
	}
	h3 em {
		font-style: normal;
		color: var(--ink, #e8e6e1);
	}
	h3 .muted {
		text-transform: none;
		letter-spacing: 0;
	}
	.clear {
		margin-left: auto;
		font: 11px/1 system-ui, sans-serif;
		padding: 4px 8px;
		border-radius: 999px;
		border: 1px solid var(--warn, #d9a441);
		background: transparent;
		color: var(--warn, #d9a441);
		cursor: pointer;
		text-transform: none;
		letter-spacing: 0;
	}
	.clear:disabled {
		opacity: 0.4;
		cursor: default;
	}
	p {
		margin: 0 10px;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0 6px;
	}
	.active li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		padding: 3px 4px 3px 6px;
		border-left: 2px solid var(--warn, #d9a441);
		margin: 2px 0;
	}
	.active button {
		border: 0;
		background: transparent;
		color: var(--ink-2, #a8a6a1);
		cursor: pointer;
		font-size: 14px;
		line-height: 1;
		padding: 2px 6px;
		border-radius: 4px;
	}
	.active button:hover {
		color: var(--ink, #e8e6e1);
		background: var(--axis, #383835);
	}
	.list {
		flex: 1 1 auto;
		overflow-y: auto;
		padding-bottom: 6px;
		min-height: 0;
	}
	.list button {
		display: block;
		width: 100%;
		text-align: left;
		padding: 5px 6px;
		border: 1px solid transparent;
		border-radius: 6px;
		background: transparent;
		color: inherit;
		font: inherit;
		cursor: pointer;
	}
	.list button:hover {
		background: color-mix(in srgb, var(--axis, #383835) 60%, transparent);
	}
	.list button.on {
		border-color: var(--warn, #d9a441);
	}
	.list b {
		display: block;
	}
	.list span {
		color: var(--ink-2, #a8a6a1);
	}
</style>
