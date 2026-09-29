<script lang="ts">
	// A server, drawn from its chassis profile crossed with its tags
	// (docs/design/spatial-hmi.md §3e). The chassis is one node (the Server
	// UDT: `{node}`); every bay, slot, socket, fan and PSU in the profile is
	// a node of its own — `{node}/{part id}`, bound to the tag the profile's
	// `bindings` name — so each part picks, halos and greys on its own. That
	// is why the parts are siblings of the chassis node and not parts of it
	// (§3d: a part inside a node picks the node).
	//
	// Place it inside <Scene3D> / <SceneView> like any <Node>. Views: `lid`
	// on or off, `xray` (the shell at 20 %, clicks pass through it),
	// `exploded` (each part moves by its profile offset).
	import { T } from '@threlte/core';
	import { HTML } from '@threlte/extras';
	import { getContext } from 'svelte';
	import { SCENE, type SceneContext } from '../context.js';
	import { DEFAULT_PALETTE, type Palette } from '../palette.js';
	import { member } from './profile.js';
	import { overlayColors, heatPaint, sensorLimits, placeLabels, fanOutLabels, type Overlay, type OverlayColors, type OverlayContext, type PartPaint } from './overlay.js';
	import { Tween } from 'svelte/motion';
	import { cubicOut } from 'svelte/easing';
	import type { Component } from 'svelte';
	import Node from '../components/Node.svelte';
	import Fade from '../components/Fade.svelte';
	import type { Vec3 } from '../scene.js';
	import { mm, resolveParts, type ChassisProfile, type PartKind, tagFor } from './profile.js';
	import { DEFAULT_MODELS } from './look.js';
	import Chassis from './Chassis.svelte';
	import Drive from './Drive.svelte';
	import Dimm from './Dimm.svelte';
	import PcieCard from './PcieCard.svelte';
	import Fan from './Fan.svelte';
	import Psu from './Psu.svelte';
	import Cpu from './Cpu.svelte';
	import Port from './Port.svelte';

	let {
		profile,
		node,
		label,
		pos = [0, 0, 0],
		rot,
		lid = 'on',
		xray = false,
		exploded = false,
		anchor = true,
		codeImages,
		overlay,
		kind = 'server',
		fade = 1,
		models = DEFAULT_MODELS
	}: {
		profile: ChassisProfile;
		/** The node's tag prefix, e.g. `NODE1`: the chassis tag and every
		 * part's `{node}`. */
		node: string;
		label?: string;
		pos?: Vec3;
		rot?: Vec3;
		lid?: 'on' | 'off';
		xray?: boolean;
		exploded?: boolean;
		/** Draw the printed codes at the profile's anchors. */
		anchor?: boolean;
		/** Anchor id → an image of its printed code; see Chassis. */
		codeImages?: Record<string, string>;
		/** One question asked of every part (heat, interfaces, free…): its
		 * colour and text paint the parts, the rest go faint (overlay.ts).
		 * The shell turns see-through while one is on: the answer is inside. */
		overlay?: Overlay;
		/** Draw the whole server at this opacity (1 = as it is): the rest of a
		 * rack while one device has the focus. Faint ones do not pick. */
		fade?: number;
		/** The chassis node's kind (`server`, `switch`). */
		kind?: string;
		/** Where the part library (models/hardware/*.glb) is served. */
		models?: string;
	} = $props();

	const COMPONENTS: Record<PartKind, Component<any>> = { drive: Drive, dimm: Dimm, 'pcie-card': PcieCard, fan: Fan, psu: Psu, cpu: Cpu, port: Port };
	const deg = Math.PI / 180;

	let parts = $derived(resolveParts(profile, node));
	let size = $derived(mm(profile.size));
	let chassisTag = $derived(profile.bindings.chassis ? tagFor(profile.bindings.chassis, node) : undefined);
	let chassisBox = $derived({ size: size, center: [0, size[1] / 2, -size[2] / 2] as Vec3 });

	const t = Tween.of(() => (exploded ? 1 : 0), { duration: 700, easing: cubicOut });
	const at = (p: Vec3, o: Vec3, f: number): Vec3 => [p[0] + o[0] * f, p[1] + o[1] * f, p[2] + o[2] * f];
	// The overlay reads the frame the scene provides, like every <Node>.
	const scene = getContext<SceneContext | undefined>(SCENE);
	const palette = getContext<Palette>('hmi3d:palette') ?? DEFAULT_PALETTE;
	let colors = $derived<OverlayColors>(overlayColors(palette));
	let octx = $derived<OverlayContext>({ node, profile, tags: scene?.tags ?? {}, colors });
	let paints = $derived.by(() => {
		const out: Record<string, PartPaint | undefined> = {};
		if (!overlay) return out;
		for (const part of parts) out[part.id] = overlay.paint(part, part.tag ? octx.tags[part.tag] : undefined, octx);
		return out;
	});
	// Standalone sensors (inlet, VRM…), painted against their own setpoints.
	let sensors = $derived(
		overlay?.sensors
			? (profile.sensors ?? []).map((q) => {
					const v = octx.tags[tagFor(q.tag, node)];
					const t = member(v, 'Value');
					const lim = sensorLimits(v);
					const paint = typeof t === 'number' && lim ? heatPaint(t, lim[0], lim[1], q.id === 'inlet' ? t : inletOf(), colors) : undefined;
					return { ...q, pos: mm(q.pos), paint };
				})
			: []
	);
	const inletOf = () => {
		const v = member(octx.tags[node], 'InletTempC');
		return typeof v === 'number' ? v : 25;
	};
	const top = (p: Vec3, s: Vec3): Vec3 => [p[0], p[1] + s[1] / 2 + 0.006, p[2]];
	// Each part's overlay label, placed so neighbours do not overprint —
	// or, for an overlay that fans out, set out from the port face on a leader.
	let said = $derived(parts.filter((part) => paints[part.id]?.text && !paints[part.id]?.dim));
	let fanned = $derived(
		overlay?.fanOut
			? fanOutLabels(
					said
						.filter((part) => part.kind === 'port')
						.map((part) => {
							const out = Math.abs((part.rot?.[1] ?? 0) % 360) === 180 ? 1 : -1;
							const p = at(part.pos, part.explode, t.current);
							// A front panel's ports (a switch) are in two rows: the lower row's labels go down.
							const up = part.props.panel && part.pos[1] < size[1] / 2 ? -1 : 1;
							return { id: part.id, at: [p[0], p[1], p[2] + (out * part.size[2]) / 2] as Vec3, out: out as 1 | -1, up: up as 1 | -1 };
						})
				)
			: new Map<string, { from: Vec3; to: Vec3 }>()
	);
	let labels = $derived(
		new Map([
			...placeLabels(said.filter((part) => !fanned.has(part.id)).map((part) => ({ id: part.id, at: top(at(part.pos, part.explode, t.current), part.size), text: paints[part.id]!.text! }))),
			...[...fanned].map(([id, f]) => [id, f.to] as [string, Vec3])
		])
	);
	// A leader from a port mouth to its label: a thin bar, turned about x.
	const leader = (f: { from: Vec3; to: Vec3 }) => {
		const dy = f.to[1] - f.from[1];
		const dz = f.to[2] - f.from[2];
		return { pos: [f.from[0], f.from[1] + dy / 2, f.from[2] + dz / 2] as Vec3, len: Math.hypot(dy, dz), rx: Math.atan2(-dy, dz) };
	};

	let lidOffset = $derived(mm(profile.explodeLid ?? [0, 160, 0]).map((v) => v * t.current) as Vec3);
</script>

<T.Group position={pos} rotation={rot ? [rot[0] * deg, rot[1] * deg, rot[2] * deg] : [0, 0, 0]}>
<Fade amount={fade}>
	<Node id={node} tag={chassisTag} {kind} label={overlay ? '' : (label ?? node)} pos={[0, 0, 0]} bounds={chassisBox}>
		{#snippet children(p)}
			<Chassis {profile} {lid} xray={xray || !!overlay} {lidOffset} {anchor} {codeImages} good={p.good} />
		{/snippet}
	</Node>
	{#each parts as part (part.id)}
		{@const Part = COMPONENTS[part.kind]}
		<Node
			id={part.id}
			tag={part.tag}
			kind={part.kind}
			label=""
			pos={at(part.pos, part.explode, t.current)}
			rot={part.rot}
			bounds={{ size: part.size, center: [0, 0, 0] }}
			props={{ ...part.props, size: part.size, bound: part.tag !== undefined, static: part.static, models, xray, paint: paints[part.id] }}
		>
			{#snippet children(p)}
				<Part {...p} />
			{/snippet}
		</Node>
		{@const paint = paints[part.id]}
		{@const where = labels.get(part.id)}
		{@const fan = fanned.get(part.id)}
		{#if fan && paint?.color}
			{@const l = leader(fan)}
			<T.Mesh position={l.pos} rotation={[l.rx, 0, 0]} raycast={() => {}}>
				<T.BoxGeometry args={[0.0007, 0.0007, l.len]} />
				<T.MeshBasicMaterial color={paint.color} />
			</T.Mesh>
		{/if}
		{#if paint?.text && where}
			<HTML position={where} center pointerEvents="none">
				<span class="ov" style:--c={paint.color ?? '#8a8d91'}>{paint.text}</span>
			</HTML>
		{/if}
	{/each}
	{#each sensors as q (q.id)}
		<T.Mesh position={q.pos} raycast={() => {}}>
			<T.SphereGeometry args={[0.005, 16, 12]} />
			<T.MeshBasicMaterial color={q.paint?.color ?? '#6b6b68'} />
		</T.Mesh>
		<HTML position={[q.pos[0], q.pos[1] + 0.012, q.pos[2]]} center pointerEvents="none">
			<span class="ov sensor" style:--c={q.paint?.color ?? '#6b6b68'}>{q.name} {q.paint?.text ?? '—'}</span>
		</HTML>
	{/each}
</Fade>
</T.Group>

<style>
	/* An overlay's value on a part: small, the text in ink, the colour a
	   swatch beside it — colour never carries the meaning alone. */
	.ov {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 1px 5px;
		border-radius: 4px;
		font: 600 10px/1.3 ui-monospace, monospace;
		white-space: nowrap;
		color: var(--ink, #e8e6e1);
		background: color-mix(in srgb, var(--surface, #1a1a19) 82%, transparent);
		border: 1px solid color-mix(in srgb, var(--c) 70%, transparent);
	}
	.ov::before {
		content: '';
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--c);
	}
	.sensor {
		font-weight: 500;
		opacity: 0.9;
	}
</style>
