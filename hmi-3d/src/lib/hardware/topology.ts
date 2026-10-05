// Cabling (docs/design/spatial-hmi.md §3e): which port goes to which, as a
// site's topology declares it — devices by id, links between exact ports
// (`node1/nicSlot2p2 ↔ sw1/te0/25`) — checked against what each end
// reports live. The declaration is the plan; the tags are the evidence:
//
// - `confirmed`     an end names the other: an LLDP neighbour, or the far
//                   end's MAC learned on the switch port
// - `consistent`    both ends up, at the same speed (and the declared rate)
// - `contradicted`  the evidence disagrees: one end up and the other down,
//                   the speeds differ, or LLDP names a different neighbour
// - `down`          no end that reports has link
// - `unverified`    an end is not reported, so nothing can be said
//
// An end whose device is offline (`<device>__Online` false) is not
// reported: its tags hold their last values, which say nothing about now.
// A dark switch's cables read down (the far end has no link) or
// unverified, never contradicted by the switch's stale "up".
//
// Pure: no Svelte, no three, like profile.ts.
import type { ChassisProfile, ProfilePart } from './profile.js';
import { member, portReading, tagFor } from './profile.js';

export interface TopoDevice {
	id: string;
	kind: string;
	/** Chassis profile id, when the topology knows it. */
	profile?: string;
	model?: string;
	hostname?: string;
	/** The device's tag prefix (`NODE1`, `SW1`). */
	tag: string;
}

export interface TopoLink {
	kind: string;
	/** `device/port`: a part id or its `port` name in the device's profile.
	 * A bare name (`site`) is a far end outside the model. */
	a: string;
	b: string;
	rateGbps?: number;
	/** How the declaration was made (a MAC table, LLDP, a design doc). */
	evidence?: string;
	/** The server port's MAC, as the declaration saw it. */
	mac?: string;
	name?: string;
	/** What the link should carry (vlan.ts): `null` for a link with no
	 * VLAN at all (a storage mesh), absent when the plan does not say. */
	vlans?: LinkVlans | null;
	/** The state the plan expects: `down` for a port left open on purpose
	 * (a service port a visitor plugs a laptop into). Down is then as
	 * declared, and link on it is the surprise. Absent: up. */
	expect?: 'up' | 'down';
	[k: string]: unknown;
}

/** A link's VLANs as the plan declares them: the VLANs it carries tagged
 * (a trunk) and, when the plan says, its native (untagged) VLAN — an
 * access port is `{ native: 22 }` and carries nothing tagged. */
export interface LinkVlans {
	tagged?: number[];
	native?: number;
	/** Where the declaration came from, when not the topology's own. */
	evidence?: string;
}

/** A VLAN the site plans for. */
export interface TopoVlan {
	id: number;
	name: string;
	note?: string;
}

export interface Topology {
	site: string;
	generated?: string;
	devices: TopoDevice[];
	links: TopoLink[];
	vlans?: TopoVlan[];
}

/** A topology with each device's profile to hand. */
export interface Plant {
	topology: Topology;
	profileOf(d: TopoDevice): ChassisProfile | undefined;
}

export interface PortRef {
	device: string;
	port?: string;
}

export const parseEnd = (s: string): PortRef => {
	const i = s.indexOf('/');
	return i < 0 ? { device: s } : { device: s.slice(0, i), port: s.slice(i + 1) };
};

/** A profile's part for a port name: its id, or the `port` a cable plan
 * calls it by (`te0/25`, `slot2 p2`). */
export function portPart(p: ChassisProfile, port: string): ProfilePart | undefined {
	return p.parts.find((q) => q.id === port) ?? p.parts.find((q) => q.port === port);
}

/** What a cable label calls a port: its `port` name, else its `short`, else its id. */
export function portName(q: ProfilePart): string {
	return typeof q.port === 'string' ? q.port : typeof q.short === 'string' ? q.short : q.id;
}

export const deviceById = (t: Topology, id: string) => t.devices.find((d) => d.id === id);
export const deviceByTag = (t: Topology, tag: string) => t.devices.find((d) => d.tag === tag);

/** One end of a link, resolved: its device, profile part and tag. */
export interface End {
	ref: PortRef;
	device?: TopoDevice;
	part?: ProfilePart;
	tag?: string;
	/** `sw1 te0/25`, `node2 slot3 p1`, `site`. */
	label: string;
}

export function resolveEnd(plant: Plant, s: string): End {
	const ref = parseEnd(s);
	const device = deviceById(plant.topology, ref.device);
	const profile = device && plant.profileOf(device);
	const part = profile && ref.port ? portPart(profile, ref.port) : undefined;
	const binding = part && profile ? profile.bindings[part.id] : undefined;
	const tag = binding && device ? tagFor(binding, device.tag) : undefined;
	const label = ref.port ? `${ref.device} ${part ? portName(part) : ref.port}` : ref.device;
	return { ref, device, part, tag, label };
}

/** The link on a device's port, and which side of it the port is. */
export function linkAt(t: Topology, device: string, part: ProfilePart): { link: TopoLink; near: 'a' | 'b' } | undefined {
	const names = new Set([part.id, portName(part)]);
	for (const link of t.links) {
		for (const near of ['a', 'b'] as const) {
			const r = parseEnd(link[near]);
			if (r.device === device && r.port !== undefined && names.has(r.port)) return { link, near };
		}
	}
	return undefined;
}

// ── the check ──────────────────────────────────────────────────────────

/** One end as its tag reports it, from either UDT: a server NetPort
 * (LinkUp, SpeedGbps, MAC) or a switch SwitchPort (OperUp, SpeedMbps).
 * `LldpSystem` / `LldpPort` (the neighbour LLDP reports) and `Macs` (MACs
 * learned on the port) are read when a driver publishes them. */
export interface Reading {
	reported: boolean;
	up?: boolean;
	gbps?: number;
	mac?: string;
	lldp?: { system: string; port?: string };
	macs?: string[];
}

const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined);
const normMac = (m: string) => m.toLowerCase().replace(/[^0-9a-f]/g, '');
const isMac = (m: string | undefined): m is string => !!m && normMac(m).length === 12;

export function readEnd(v: unknown): Reading {
	if (v === undefined || v === null) return { reported: false };
	const { up, gbps } = portReading(v);
	const system = str(member(v, 'LldpSystem'));
	const macs = member(v, 'Macs');
	return {
		reported: true,
		up,
		gbps,
		mac: str(member(v, 'MAC')),
		lldp: system ? { system, port: str(member(v, 'LldpPort')) } : undefined,
		macs: Array.isArray(macs) ? macs.filter((m): m is string => typeof m === 'string') : undefined
	};
}

export type Verdict = 'confirmed' | 'consistent' | 'contradicted' | 'down' | 'unverified';

export interface LinkCheck {
	verdict: Verdict;
	/** Why, in words, most telling first. */
	reasons: string[];
	a: End & Reading;
	b: End & Reading;
}

const gtext = (g: number) => (g >= 1 ? `${+g.toFixed(1)}G` : `${Math.round(g * 1000)}M`);
/** Does an LLDP neighbour name this end? By hostname or device id. */
const names = (seen: { system: string; port?: string }, e: End) => {
	const sys = seen.system.toLowerCase();
	return [e.device?.hostname, e.device?.id].some((n) => n && (sys === n.toLowerCase() || sys.startsWith(`${n.toLowerCase()}.`)));
};

/** Check one declared link against what its ends report. `tags` is the
 * frame (name → value). */
export function checkLink(plant: Plant, link: TopoLink, tags: Record<string, unknown>): LinkCheck {
	const c = checkWired(plant, link, tags);
	if (link.expect !== 'down') return c;
	// A link declared down: no link is the plan, link is someone plugged in.
	const live = [c.a, c.b].find((e) => e.up === true);
	if (live) return { ...c, verdict: 'contradicted', reasons: [`${live.label} has link, declared down: something is plugged in`, ...c.reasons] };
	if (c.verdict === 'down') return { ...c, verdict: 'consistent', reasons: ['down, as declared', ...c.reasons] };
	return c;
}

function checkWired(plant: Plant, link: TopoLink, tags: Record<string, unknown>): LinkCheck {
	const ea = resolveEnd(plant, link.a);
	const eb = resolveEnd(plant, link.b);
	const reasons: string[] = [];
	const offline = (e: End) => !!e.device?.tag && tags[`${e.device.tag}__Online`] === false;
	const read = (e: End) => {
		if (!e.tag) return readEnd(undefined);
		if (offline(e)) {
			reasons.push(`${e.device!.tag} is offline: ${e.label}'s last values say nothing about now`);
			return readEnd(undefined);
		}
		return readEnd(tags[e.tag]);
	};
	const a = { ...ea, ...read(ea) };
	const b = { ...eb, ...read(eb) };
	const done = (verdict: Verdict): LinkCheck => ({ verdict, reasons, a, b });

	// 1. An end that names its neighbour settles it, either way.
	for (const [near, far] of [
		[a, b],
		[b, a]
	] as const) {
		if (!near.lldp) continue;
		if (!far.device) {
			// A far end outside the model has no name we know: its LLDP can
			// neither confirm nor contradict. Said, then judged on the end in it.
			reasons.push(`${near.label} sees ${near.lldp.system}${near.lldp.port ? ` ${near.lldp.port}` : ''} over LLDP`);
			continue;
		}
		if (names(near.lldp, far)) {
			reasons.push(`${near.label} sees ${near.lldp.system}${near.lldp.port ? ` ${near.lldp.port}` : ''} over LLDP`);
			return done('confirmed');
		}
		reasons.push(`${near.label} sees ${near.lldp.system}${near.lldp.port ? ` ${near.lldp.port}` : ''} over LLDP, not ${far.device?.hostname ?? far.device?.id ?? far.label}`);
		return done('contradicted');
	}
	for (const [near, far] of [
		[a, b],
		[b, a]
	] as const) {
		const mac = isMac(far.mac) ? far.mac : isMac(link.mac) ? link.mac : undefined;
		if (!near.macs || !mac) continue;
		if (near.macs.some((m) => normMac(m) === normMac(mac))) {
			reasons.push(`${far.label}'s MAC is learned on ${near.label}`);
			return done('confirmed');
		}
	}

	// 2. One end outside the model (the site's uplink, a provider's
	// hand-off) can never report: the end we model is the whole evidence.
	// Up at the declared rate is all a declared link can be asked for.
	const outside = [a, b].filter((e) => !e.device);
	const inside = [a, b].find((e) => e.device);
	if (outside.length === 1 && inside?.reported) {
		reasons.push(`${outside[0].label} is outside the model`);
		if (inside.up === false) {
			reasons.push(`${inside.label} has no link`);
			return done('down');
		}
		if (inside.up === true) {
			if (inside.gbps !== undefined && link.rateGbps !== undefined && Math.abs(inside.gbps - link.rateGbps) > 1e-6) {
				reasons.push(`${inside.label} up at ${gtext(inside.gbps)}, declared ${gtext(link.rateGbps)}`);
				return done('contradicted');
			}
			reasons.push(`${inside.label} up${inside.gbps !== undefined ? ` at ${gtext(inside.gbps)}` : ''}`);
			return done('consistent');
		}
	}

	// 3. Both ends' link state and speed.
	if (!a.reported || !b.reported) {
		const silent = [a, b].filter((e) => !e.reported).map((e) => (e.ref.port ? e.label : `${e.label} (outside the model)`));
		reasons.push(`not reported: ${silent.join(', ')}`);
		const other = a.reported ? a : b.reported ? b : undefined;
		if (other?.up !== undefined) reasons.push(`${other.label} ${other.up ? `up${other.gbps ? ` at ${gtext(other.gbps)}` : ''}` : 'down'}`);
		// The end we can see has no link: whatever the other end says, this
		// cable is not carrying anything.
		return done(other?.up === false ? 'down' : 'unverified');
	}
	// An end that reports but carries no link member (no LinkUp/OperUp)
	// says nothing about link: never "both up", never a contradiction.
	if (a.up === undefined || b.up === undefined) {
		const mute = [a, b].filter((e) => e.up === undefined);
		reasons.push(`no link state from ${mute.map((e) => e.label).join(', ')}`);
		const other = [a, b].find((e) => e.up !== undefined);
		if (other) reasons.push(`${other.label} ${other.up ? 'up' : 'down'}`);
		return done(other?.up === false ? 'down' : 'unverified');
	}
	if (a.up === false && b.up === false) {
		reasons.push('neither end has link');
		return done('down');
	}
	if (a.up !== b.up) {
		const [up, dn] = a.up ? [a, b] : [b, a];
		reasons.push(`${up.label} is up, ${dn.label} is down`);
		return done('contradicted');
	}
	if (a.gbps !== undefined && b.gbps !== undefined && Math.abs(a.gbps - b.gbps) > 1e-6) {
		reasons.push(`${a.label} at ${gtext(a.gbps)}, ${b.label} at ${gtext(b.gbps)}`);
		return done('contradicted');
	}
	const g = a.gbps ?? b.gbps;
	if (g !== undefined && link.rateGbps !== undefined && Math.abs(g - link.rateGbps) > 1e-6) {
		reasons.push(`both up at ${gtext(g)}, declared ${gtext(link.rateGbps)}`);
		return done('contradicted');
	}
	reasons.push(`both ends up${g !== undefined ? ` at ${gtext(g)}` : ''}`);
	return done('consistent');
}

/** Every declared link in the model, checked. */
export function checkAll(plant: Plant, tags: Record<string, unknown>): { link: TopoLink; check: LinkCheck }[] {
	return plant.topology.links.map((link) => ({ link, check: checkLink(plant, link, tags) }));
}

/** Every tag the cabling reads: both ends of every link. */
export function linkTags(plant: Plant): string[] {
	const out = new Set<string>();
	for (const l of plant.topology.links) for (const s of [l.a, l.b]) {
		const t = resolveEnd(plant, s).tag;
		if (t) out.add(t);
	}
	return [...out];
}

export const VERDICT_MARK: Record<Verdict, string> = { confirmed: '✓', consistent: '=', contradicted: '✗', down: '↓', unverified: '?' };

const VERDICT_TEXT: Record<Verdict, string> = {
	confirmed: 'Confirmed',
	consistent: 'Consistent',
	contradicted: 'Contradicted',
	down: 'Down',
	unverified: 'Unverified'
};

/** A port faceplate's cable rows: where it goes, the verdict and why, and
 * where the declaration came from. */
export function linkFacts(check: LinkCheck, near: 'a' | 'b', link: TopoLink): { label: string; value: string }[] {
	const far = near === 'a' ? check.b : check.a;
	const rows = [
		{ label: 'Cable to', value: far.label },
		{ label: 'Link', value: `${link.kind}${link.name ? ` ${link.name}` : ''}${link.rateGbps ? ` · ${gtext(link.rateGbps)} declared` : ''}` },
		{ label: 'Check', value: `${VERDICT_MARK[check.verdict]} ${VERDICT_TEXT[check.verdict]}` },
		...check.reasons.map((r) => ({ label: '', value: r }))
	];
	if (link.evidence) rows.push({ label: 'Declared from', value: link.evidence });
	return rows;
}

/** A device's neighbourhood, for a focused view: the devices its cables
 * reach and the indices of those links. Far ends outside the model (the
 * site) count as neighbours by name. */
export function neighbourhood(t: Topology, device: string): { neighbours: Set<string>; links: Set<number> } {
	const neighbours = new Set<string>();
	const links = new Set<number>();
	t.links.forEach((l, i) => {
		const a = parseEnd(l.a).device;
		const b = parseEnd(l.b).device;
		if (a !== device && b !== device) return;
		links.add(i);
		neighbours.add(a === device ? b : a);
	});
	neighbours.delete(device);
	return { neighbours, links };
}

/** How strongly to draw a device while `focus` has the focus: itself in
 * full, what its cables reach readable, the rest very faint. */
export function focusFade(t: Topology, focus: string | undefined, device: string): number {
	if (!focus || focus === device) return 1;
	return neighbourhood(t, focus).neighbours.has(device) ? 0.4 : 0.1;
}
