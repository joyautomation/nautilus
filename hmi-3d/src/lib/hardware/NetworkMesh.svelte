<script lang="ts">
	// The mesh view: a topology as a network floating in space (mesh.ts) —
	// each device a node that picks like any <Node> (its id is its tag), each
	// declared link an arc coloured by its live check, labelled with the two
	// ports it joins. The same checks as the rack's cables, so the two views
	// can never disagree.
	import { T } from '@threlte/core';
	import { HTML } from '@threlte/extras';
	import Node from '../components/Node.svelte';
	import Fade from '../components/Fade.svelte';
	import Cable from './Cable.svelte';
	import { meshLayout } from './mesh.js';
	import { VERDICT_MARK, deviceById, focusFade, neighbourhood, type LinkCheck, type Topology } from './topology.js';
	import { verdictColor, type OverlayColors } from './overlay.js';
	import type { Vec3 } from '../scene.js';

	let {
		topology,
		checks,
		colors,
		summary = {},
		labels = true,
		focus,
		pos = [0, 0, 0]
	}: {
		topology: Topology;
		/** checkAll()'s results, in the topology's link order. */
		checks: { check: LinkCheck }[];
		colors: OverlayColors;
		/** A line under each device's name (`6/28 up`, `212 W`), by device id. */
		summary?: Record<string, string>;
		/** Label every edge with its ports. */
		labels?: boolean;
		/** A device id in focus: it and its links in full, what they reach
		 * readable, the rest very faint and unpickable. */
		focus?: string;
		pos?: Vec3;
	} = $props();

	let layout = $derived(meshLayout(topology));
	const SIZE: Record<string, Vec3> = { server: [0.2, 0.045, 0.13], switch: [0.22, 0.03, 0.1], outside: [0.06, 0.06, 0.06] };
	const COLOR: Record<string, string> = { server: '#8a8d91', switch: '#2b2d30', outside: '#4a4c50' };
	let hood = $derived(focus ? neighbourhood(topology, focus) : undefined);
	const port = (label: string) => label.split(' ').slice(1).join(' ') || label;
</script>

<T.Group position={pos}>
	{#each layout.nodes as n (n.id)}
		{@const d = deviceById(topology, n.id)}
		{@const size = SIZE[n.kind] ?? SIZE.server}
		{@const k = focusFade(topology, focus, n.id)}
		{#if d}
			<Fade amount={k}>
			<Node id={d.tag} tag={d.tag} kind={d.kind} label="" pos={n.pos} bounds={{ size, center: [0, 0, 0] }}>
				{#snippet children()}
					<T.Mesh>
						<T.BoxGeometry args={size} />
						<T.MeshStandardMaterial color={COLOR[n.kind] ?? COLOR.server} metalness={0.3} roughness={0.5} />
					</T.Mesh>
				{/snippet}
			</Node>
			</Fade>
		{:else}
			<T.Mesh position={n.pos} raycast={() => {}}>
				<T.SphereGeometry args={[0.03, 24, 16]} />
				<T.MeshStandardMaterial color={COLOR.outside} transparent={k < 1} opacity={k} />
			</T.Mesh>
		{/if}
		<HTML position={[n.pos[0], n.pos[1] + size[1] / 2 + 0.05, n.pos[2]]} center pointerEvents="none">
			<span class="dev" style:opacity={k < 1 ? Math.max(k, 0.25) : 1}>
				<b>{d?.hostname ?? n.id}</b>
				{#if summary[n.id]}<small>{summary[n.id]}</small>{/if}
			</span>
		</HTML>
	{/each}
	{#each layout.edges as e (e.index)}
		{@const c = checks[e.index]?.check}
		{#if c}
			{@const color = verdictColor(c.verdict, colors)}
			{@const near = !hood || hood.links.has(e.index)}
			<Cable points={e.points} {color} arc radius={0.005} faint={c.verdict === 'unverified'} opacity={near ? 1 : 0.1} />
			{#if labels && near}
				<HTML position={e.points[1]} center pointerEvents="none">
					<span class="edge" style:--c={color} title={c.reasons.join('; ')}>{VERDICT_MARK[c.verdict]} {port(c.a.label)} ↔ {port(c.b.label)}</span>
				</HTML>
			{/if}
		{/if}
	{/each}
</T.Group>

<style>
	.dev {
		display: grid;
		justify-items: center;
		gap: 1px;
		font: 600 12px/1.2 system-ui, sans-serif;
		color: var(--ink, #e8e6e1);
		white-space: nowrap;
		text-shadow: 0 1px 2px #000;
	}
	.dev small {
		font: 500 10px/1.2 ui-monospace, monospace;
		color: var(--ink-2, #a8a6a1);
	}
	.edge {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 1px 5px;
		border-radius: 4px;
		font: 500 10px/1.3 ui-monospace, monospace;
		white-space: nowrap;
		color: var(--ink, #e8e6e1);
		background: color-mix(in srgb, var(--surface, #1a1a19) 82%, transparent);
		border: 1px solid color-mix(in srgb, var(--c) 70%, transparent);
	}
</style>
