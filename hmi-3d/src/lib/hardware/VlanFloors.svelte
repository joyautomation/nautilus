<script lang="ts">
	// The VLAN view: each VLAN a floor (vlan.ts vlanFloors), stacked, holding
	// the devices and links that carry it, laid out as the mesh view lays
	// them out, so a device keeps its place on every floor and a post joins
	// it through the floors it is on. A link the plan declares that does not
	// carry the VLAN is drawn faint red, one that carries it undeclared
	// amber; the ring's blocked protection link grey. A VLAN split by a
	// missing trunk shows as islands: the devices cut off from the biggest
	// one in red. Click a floor's name to pick the VLAN.
	import { T } from '@threlte/core';
	import { HTML } from '@threlte/extras';
	import Fade from '../components/Fade.svelte';
	import Cable from './Cable.svelte';
	import { deviceById, type Topology } from './topology.js';
	import { islandsOf, type VlanFloor } from './vlan.js';
	import type { OverlayColors } from './overlay.js';
	import type { Vec3 } from '../scene.js';

	let {
		topology,
		floors,
		colors,
		vlanColor,
		pick,
		onpick,
		spread = 1,
		visible = true,
		pos = [0, 0, 0]
	}: {
		topology: Topology;
		floors: VlanFloor[];
		colors: OverlayColors;
		/** Each VLAN's colour (vlan.ts vlanColors). */
		vlanColor: Map<number, string>;
		/** A picked VLAN: its floor in full, the rest faint. */
		pick?: number;
		onpick?: (id: number) => void;
		/** How far the floors have opened out (vlanFloors' spread): the
		 * labels fade in as they do, so they never pile up on one plane. */
		spread?: number;
		/** Drawn at all: false keeps it mounted but unseen (the view opening). */
		visible?: boolean;
		pos?: Vec3;
	} = $props();

	const SIZE: Record<string, Vec3> = { server: [0.16, 0.03, 0.1], switch: [0.18, 0.022, 0.08], outside: [0.05, 0.05, 0.05] };
	const name = (id: string) => deviceById(topology, id)?.hostname ?? id;
	// Each device's post, from the lowest floor it is on to the highest.
	let posts = $derived.by(() => {
		const span = new Map<string, { at: Vec3; lo: number; hi: number }>();
		for (const f of floors)
			for (const n of f.nodes) {
				const s = span.get(n.id);
				if (!s) span.set(n.id, { at: n.pos, lo: f.y, hi: f.y });
				else ((s.lo = Math.min(s.lo, f.y)), (s.hi = Math.max(s.hi, f.y)));
			}
		return [...span.entries()].filter(([, s]) => s.hi - s.lo > 1e-6);
	});
	const edgeColor = (l: VlanFloor['edges'][number]['link'], c: string) => (l.blocked ? colors.neutral : !l.carried ? colors.critical : !l.declared ? colors.warning : c);
</script>

<T.Group position={pos} {visible}>
	{#each posts as [id, s] (id)}
		<T.Mesh position={[s.at[0], (s.lo + s.hi) / 2, s.at[2]]} raycast={() => {}}>
			<T.CylinderGeometry args={[0.0025, 0.0025, s.hi - s.lo, 6]} />
			<T.MeshBasicMaterial color={colors.neutral} transparent opacity={0.35} />
		</T.Mesh>
	{/each}
	{#each floors as f, i (f.domain.id)}
		{@const c = vlanColor.get(f.domain.id) ?? colors.neutral}
		{@const k = pick === undefined || pick === f.domain.id ? 1 : 0.15}
		{@const split = f.domain.islands.length > 1}
		<Fade amount={k}>
			<T.Mesh position={[0, f.y - 0.02, 0]} rotation={[-Math.PI / 2, 0, 0]} raycast={() => {}}>
				<T.CircleGeometry args={[1.2, 64]} />
				<T.MeshBasicMaterial color={c} transparent opacity={pick === f.domain.id ? 0.14 : 0.06} depthWrite={false} />
			</T.Mesh>
			{#each f.nodes as n (n.id)}
				{@const isl = islandsOf(f.domain, n.id)}
				{@const size = SIZE[n.kind] ?? SIZE.server}
				<T.Mesh position={n.pos} raycast={() => {}}>
					<T.BoxGeometry args={size} />
					<T.MeshStandardMaterial color={!split || (isl.length === 1 && isl[0] === 0) ? c : isl.includes(0) ? colors.warning : colors.critical} metalness={0.2} roughness={0.6} />
				</T.Mesh>
				{#if (i === 0 || pick === f.domain.id) && spread > 0.6}
					<HTML position={[n.pos[0], n.pos[1] + 0.05, n.pos[2]]} center pointerEvents="none">
						<span class="dev" style:opacity={k < 1 ? 0.3 : 1}>{name(n.id)}</span>
					</HTML>
				{/if}
			{/each}
		</Fade>
		{#each f.edges as e (e.link.index)}
			<Cable points={e.points} color={edgeColor(e.link, c)} arc radius={0.004} faint={e.link.blocked || !e.link.carried} opacity={k} />
		{/each}
		<HTML position={[-1.3, f.y, 0]} center>
			<button class="floor" class:on={pick === f.domain.id} style:--c={c} style:opacity={(k < 1 ? 0.55 : 1) * Math.min(1, Math.max(0, (spread - 0.6) / 0.4))} onclick={() => onpick?.(f.domain.id)}>
				<i></i>
				<b>{f.domain.id}</b>
				<span>{f.domain.name ?? ''}{f.domain.declared ? '' : ' · not in the plan'}</span>
				<small>{f.nodes.length} devices · {f.edges.filter((e) => e.link.carried).length} links{#if split}<em> · ✗ {f.domain.islands.length} islands</em>{/if}{#if f.edges.some((e) => !e.link.carried)}<em> · ✗ {f.edges.filter((e) => !e.link.carried).length} missing</em>{/if}</small>
			</button>
		</HTML>
	{/each}
</T.Group>

<style>
	.dev {
		font: 600 11px/1.2 system-ui, sans-serif;
		color: var(--ink, #e8e6e1);
		white-space: nowrap;
		text-shadow: 0 1px 2px #000;
	}
	.floor {
		display: grid;
		grid-template-columns: auto auto 1fr;
		align-items: center;
		column-gap: 6px;
		padding: 4px 9px;
		border-radius: 8px;
		border: 1px solid color-mix(in srgb, var(--c) 60%, transparent);
		background: color-mix(in srgb, var(--surface, #1a1a19) 85%, transparent);
		color: var(--ink, #e8e6e1);
		font: 12px/1.3 system-ui, sans-serif;
		cursor: pointer;
		white-space: nowrap;
		text-align: left;
	}
	.floor.on {
		border-color: var(--c);
		box-shadow: 0 0 0 1px var(--c);
	}
	.floor i {
		width: 10px;
		height: 10px;
		border-radius: 3px;
		background: var(--c);
	}
	.floor small {
		grid-column: 1 / -1;
		color: var(--ink-2, #a8a6a1);
		font: 10px/1.3 ui-monospace, monospace;
	}
	.floor em {
		font-style: normal;
		color: var(--critical, #e5484d);
	}
</style>
