<script lang="ts">
	// A part's faults, where it was right-clicked: the members of its
	// Sim_* structs (faults.ts faultsFor), each a toggle that writes the
	// plant. Grouped by struct: the part, the cable on it, its device.
	import { faultsFor, who } from './faults';
	import type { PlantFaults } from './faults.svelte';

	let {
		faults,
		x,
		y,
		title,
		simTags,
		onclose
	}: { faults: PlantFaults; x: number; y: number; title: string; simTags: string[]; onclose: () => void } = $props();

	let items = $derived(faultsFor(faults.state, simTags));
	let groups = $derived(simTags.map((t) => ({ tag: t, items: items.filter((f) => f.tag === t) })).filter((g) => g.items.length));
	let menu = $state<HTMLElement>();
	// Kept on screen: flipped left / up when it would run off an edge.
	let w = $state(0);
	let h = $state(0);
	let left = $derived(typeof window !== 'undefined' && x + w > window.innerWidth - 8 ? Math.max(8, x - w) : x);
	let top = $derived(typeof window !== 'undefined' && y + h > window.innerHeight - 8 ? Math.max(8, y - h) : y);

	$effect(() => {
		menu?.querySelector('button')?.focus();
	});
</script>

<svelte:window
	onkeydown={(e) => e.key === 'Escape' && onclose()}
	onpointerdown={(e) => menu && !menu.contains(e.target as Node) && onclose()}
/>

<div class="menu" role="menu" aria-label="Faults: {title}" bind:this={menu} bind:offsetWidth={w} bind:offsetHeight={h} style:left="{left}px" style:top="{top}px">
	<div class="head"><span class="sim">SIM</span> <b>{title}</b></div>
	{#each groups as g (g.tag)}
		<div class="group">{who(g.tag)}</div>
		{#each g.items as f (f.member)}
			<button role="menuitemcheckbox" aria-checked={f.on} class:on={f.on} onclick={() => faults.write(`${f.tag}.${f.member}`, f.on ? f.clear : f.set)}>
				<i>{f.on ? '✓' : ''}</i>{f.label}
			</button>
		{/each}
	{:else}
		<div class="none">No faults in the plant for this part.</div>
	{/each}
</div>

<style>
	.menu {
		position: fixed;
		z-index: 20;
		min-width: 190px;
		max-width: 280px;
		padding: 4px;
		border-radius: 8px;
		border: 1px solid var(--axis, #383835);
		background: var(--surface, #1a1a19);
		color: var(--ink, #e8e6e1);
		font: 12px/1.3 system-ui, sans-serif;
		box-shadow: 0 6px 24px rgb(0 0 0 / 0.35);
	}
	.head {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 5px 6px 6px;
		border-bottom: 1px solid var(--axis, #383835);
		margin-bottom: 2px;
	}
	.sim {
		font: 600 9px/1 system-ui, sans-serif;
		letter-spacing: 0.08em;
		padding: 2px 4px;
		border-radius: 3px;
		border: 1px solid var(--warn, #d9a441);
		color: var(--warn, #d9a441);
	}
	.group {
		padding: 6px 6px 2px;
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--ink-2, #a8a6a1);
	}
	button {
		display: flex;
		align-items: center;
		width: 100%;
		padding: 5px 6px;
		border: 0;
		border-radius: 5px;
		background: transparent;
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
	}
	button:hover,
	button:focus-visible {
		background: color-mix(in srgb, var(--axis, #383835) 70%, transparent);
		outline: none;
	}
	button.on {
		color: var(--warn, #d9a441);
	}
	i {
		width: 16px;
		font-style: normal;
	}
	.none {
		padding: 6px;
		color: var(--ink-2, #a8a6a1);
	}
</style>
