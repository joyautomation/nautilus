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
	import { Tween } from 'svelte/motion';
	import { cubicOut } from 'svelte/easing';
	import type { Component } from 'svelte';
	import Node from '../components/Node.svelte';
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
		/** Draw the QR label at the profile's anchor. */
		anchor?: boolean;
		/** Where the part library (models/hardware/*.glb) is served. */
		models?: string;
	} = $props();

	const COMPONENTS: Record<PartKind, Component<any>> = { drive: Drive, dimm: Dimm, 'pcie-card': PcieCard, fan: Fan, psu: Psu, cpu: Cpu };
	const deg = Math.PI / 180;

	let parts = $derived(resolveParts(profile, node));
	let size = $derived(mm(profile.size));
	let chassisTag = $derived(profile.bindings.chassis ? tagFor(profile.bindings.chassis, node) : undefined);
	let chassisBox = $derived({ size: size, center: [0, size[1] / 2, -size[2] / 2] as Vec3 });

	const t = Tween.of(() => (exploded ? 1 : 0), { duration: 700, easing: cubicOut });
	const at = (p: Vec3, o: Vec3, f: number): Vec3 => [p[0] + o[0] * f, p[1] + o[1] * f, p[2] + o[2] * f];
	let lidOffset = $derived(mm(profile.explodeLid ?? [0, 160, 0]).map((v) => v * t.current) as Vec3);
</script>

<T.Group position={pos} rotation={rot ? [rot[0] * deg, rot[1] * deg, rot[2] * deg] : [0, 0, 0]}>
	<Node id={node} tag={chassisTag} kind="server" label={label ?? node} pos={[0, 0, 0]} bounds={chassisBox}>
		{#snippet children(p)}
			<Chassis {profile} {lid} {xray} {lidOffset} {anchor} good={p.good} />
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
			props={{ ...part.props, size: part.size, bound: part.tag !== undefined, static: part.static, models, xray }}
		>
			{#snippet children(p)}
				<Part {...p} />
			{/snippet}
		</Node>
	{/each}
</T.Group>
