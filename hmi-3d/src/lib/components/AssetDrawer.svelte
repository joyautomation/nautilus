<script lang="ts">
	// Click-to-inspect: the kit's 2D faceplate for the node's kind, every
	// member of its struct, its quality and its alarms, in the kit's Drawer.
	// Read-only: no writes leave this panel until the write path ships.
	import { Drawer } from '@joyautomation/nautilus-hmi';
	import type { SceneNode } from '../scene.js';
	import type { NodeKindDef } from '../registry.js';
	import type { AlarmLike } from '../alarms.js';

	let {
		open = $bindable(false),
		node,
		def,
		value,
		quality = 'good',
		alarms = []
	}: {
		open?: boolean;
		node: SceneNode | undefined;
		def: NodeKindDef | undefined;
		value: unknown;
		quality?: string;
		alarms?: (AlarmLike & { name?: string })[];
	} = $props();

	let members = $derived(value && typeof value === 'object' ? Object.entries(value as Record<string, unknown>) : []);
</script>

<Drawer bind:open side="right" title={node?.label ?? node?.id ?? ''} label="Asset details">
	{#if node}
		{#if def?.panel}
			{@const Panel = def.panel}
			<Panel {value} label={node.label ?? node.id} good={quality === 'good'} />
		{/if}
		<p class="q">Quality: {quality}</p>
		{#if members.length}
			<dl>
				{#each members as [k, x]}
					<dt>{k}</dt>
					<dd>{typeof x === 'number' ? x.toFixed(2) : String(x)}</dd>
				{/each}
			</dl>
		{:else if node.tag}
			<p class="q">No value received for {node.tag} yet.</p>
		{/if}
		{#each alarms as a}
			<p class="alm">{a.priority}: {a.name ?? a.tag} ({a.state})</p>
		{/each}
	{/if}
</Drawer>

<style>
	dl {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 2px 12px;
		font: 13px ui-monospace, monospace;
	}
	dt {
		color: var(--ink-2, #a8a6a1);
	}
	dd {
		margin: 0;
	}
	.q,
	.alm {
		font-size: 13px;
	}
	.alm {
		color: var(--serious, #f5a524);
	}
</style>
