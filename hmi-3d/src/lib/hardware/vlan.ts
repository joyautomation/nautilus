// VLANs (docs/design/spatial-hmi.md §3e), the cable pattern again: the
// site's topology declares what each link should carry (`vlans` on a link:
// tagged VLANs, and the native one where the plan says), the switches say
// what their ports do (Q-BRIDGE-MIB, through the SNMP driver), and the
// check shows where they disagree.
//
// What a switch reports, per device tag `SW1`:
//
// - `SW1_Vlan20`   a Vlan: `Id`, `Name`, `Ports` (the VLAN's egress ports)
//                  and `Untagged` (those sending it untagged), each a list
//                  of front-panel positions, `"24,25,26"`
// - `SW1_Port25`   its SwitchPort's `Pvid`: the native VLAN
//
// A port's position is its SwitchPort tag's number (`SW1_Port25` → 25),
// the same numbering the driver maps the bridge's port bitmaps into.
//
// A server tags in its OS (a SET team's vNIC, a Hyper-V switch), which its
// BMC cannot see: a server end is the plan's word, never evidence.
//
// Pure: no Svelte, no three, like topology.ts.
import { member } from './profile.js';
import { meshLayout } from './mesh.js';
import type { Vec3 } from '../scene.js';
import { deviceById, parseEnd, resolveEnd, type End, type LinkCheck, type LinkVlans, type Plant, type Topology, type TopoLink } from './topology.js';

/** One switch's VLAN table: VLAN id → its name and member positions. */
export interface VlanRow {
	id: number;
	name?: string;
	ports: Set<number>;
	untagged: Set<number>;
}

/** `"24, 25,26"` → [24, 25, 26]; anything else → []. */
export function parseList(v: unknown): number[] {
	if (typeof v !== 'string') return [];
	return v
		.split(',')
		.map((s) => Number(s.trim()))
		.filter((n) => Number.isInteger(n) && n > 0);
}

/** A port tag's front-panel position: `SW1_Port07` → 7. */
export const portPosition = (tag: string | undefined): number | undefined => {
	const m = tag ? /_Port(\d+)$/.exec(tag) : null;
	return m ? Number(m[1]) : undefined;
};

const tables = new WeakMap<object, Map<string, Map<number, VlanRow>>>();

/** A device's VLAN table from the frame (empty when it reports none). One
 * pass over the frame per device, cached for the frame. */
export function vlanTable(tags: Record<string, unknown>, device: string): Map<number, VlanRow> {
	let byDevice = tables.get(tags);
	if (!byDevice) tables.set(tags, (byDevice = new Map()));
	const hit = byDevice.get(device);
	if (hit) return hit;
	const out = new Map<number, VlanRow>();
	const prefix = `${device}_Vlan`;
	for (const [k, v] of Object.entries(tags)) {
		if (!k.startsWith(prefix) || !/^\d+$/.test(k.slice(prefix.length))) continue;
		const id = Number(member(v, 'Id')) || Number(k.slice(prefix.length));
		const name = member(v, 'Name');
		out.set(id, { id, name: typeof name === 'string' && name ? name : undefined, ports: new Set(parseList(member(v, 'Ports'))), untagged: new Set(parseList(member(v, 'Untagged'))) });
	}
	byDevice.set(device, out);
	return out;
}

/** One switch port's VLANs, as its switch reports them. */
export interface PortVlans {
	reported: boolean;
	/** The PVID: the VLAN untagged frames arriving here join. */
	native?: number;
	/** VLANs sent untagged (normally just the native one). */
	untagged: number[];
	/** VLANs carried tagged. */
	tagged: number[];
}

const NONE: PortVlans = { reported: false, untagged: [], tagged: [] };

/** A switch port's VLANs: `device` is its switch's tag, `portTag` the
 * port's SwitchPort tag. Not reported while the switch is offline (its
 * tags hold their last values) or reports no VLAN table. */
export function readPortVlans(tags: Record<string, unknown>, device: string, portTag: string | undefined): PortVlans {
	const pos = portPosition(portTag);
	if (pos === undefined || tags[`${device}__Online`] === false) return NONE;
	const table = vlanTable(tags, device);
	if (!table.size) return NONE;
	const pvid = Number(member(tags[portTag!], 'Pvid'));
	const ids = [...table.keys()].sort((a, b) => a - b);
	const untagged = ids.filter((id) => table.get(id)!.untagged.has(pos));
	const tagged = ids.filter((id) => table.get(id)!.ports.has(pos) && !table.get(id)!.untagged.has(pos));
	return { reported: true, native: Number.isInteger(pvid) && pvid > 0 ? pvid : undefined, untagged, tagged };
}

/** `20 · T 21,22`: the native VLAN, then what is tagged. */
export function vlanText(v: Pick<PortVlans, 'native' | 'tagged'>): string {
	const t = v.tagged.length ? `T ${v.tagged.join(',')}` : '';
	if (v.native === undefined) return t || '–';
	return t ? `${v.native} · ${t}` : `${v.native}`;
}

/** The plan's VLANs on a link, said the same way. */
export const declaredText = (d: LinkVlans): string => vlanText({ native: d.native, tagged: d.tagged ?? [] });

// ── the check ──────────────────────────────────────────────────────────

export type VlanVerdict = 'consistent' | 'contradicted' | 'unverified' | 'none';

export interface VlanEnd extends End {
	/** A switch port, which reports its VLANs. */
	switchPort: boolean;
	vlans: PortVlans;
}

export interface VlanCheck {
	verdict: VlanVerdict;
	/** Why, in words, most telling first. */
	reasons: string[];
	declared?: LinkVlans | null;
	a: VlanEnd;
	b: VlanEnd;
	/** The VLANs that cross the link, as far as anything can tell: what
	 * both switch ends carry, else the one switch end's, else the plan's. */
	carries: number[];
	/** `carries` came from a switch, not from the plan alone. */
	observed: boolean;
}

const minus = (a: number[], b: number[]) => a.filter((x) => !b.includes(x));
const list = (xs: number[]) => xs.join(',');

/** Check one declared link's VLANs against what its switch ends report. */
export function checkVlans(plant: Plant, link: TopoLink, tags: Record<string, unknown>): VlanCheck {
	const end = (s: string): VlanEnd => {
		const e = resolveEnd(plant, s);
		const switchPort = e.device?.kind === 'switch' && !!e.tag;
		return { ...e, switchPort, vlans: switchPort ? readPortVlans(tags, e.device!.tag, e.tag) : NONE };
	};
	const a = end(link.a);
	const b = end(link.b);
	const declared = link.vlans;
	const reasons: string[] = [];
	const done = (verdict: VlanVerdict, carries: number[], observed: boolean): VlanCheck => ({ verdict, reasons, declared, a, b, carries, observed });

	if (declared === null) {
		reasons.push('declared to carry no VLAN');
		return done('none', [], false);
	}
	const want = declared?.tagged ?? [];
	const seen = [a, b].filter((e) => e.switchPort && e.vlans.reported);
	let wrong = false;

	// 1. Each switch end against the plan.
	if (declared) {
		for (const e of seen) {
			const lacks = minus(want, e.vlans.tagged);
			const extra = minus(e.vlans.tagged, want);
			if (lacks.length) reasons.push(`${e.label} lacks ${list(lacks)}`);
			if (extra.length) reasons.push(`${e.label} also carries ${list(extra)}`);
			if (declared.native !== undefined) {
				if (e.vlans.native !== declared.native) reasons.push(`${e.label} native ${e.vlans.native ?? 'none'}, declared ${declared.native}`);
				else if (!e.vlans.untagged.includes(declared.native)) reasons.push(`${e.label} does not send ${declared.native} untagged`);
			}
			wrong ||= reasons.length > 0;
		}
	}
	// 2. Two switch ends against each other, plan or none: untagged frames
	// cross a native mismatch into the other VLAN.
	if (seen.length === 2) {
		const [x, y] = seen;
		if (x.vlans.native !== y.vlans.native) {
			reasons.push(`native VLAN mismatch: ${x.label} ${x.vlans.native ?? 'none'}, ${y.label} ${y.vlans.native ?? 'none'}`);
			wrong = true;
		}
		if (!declared) {
			for (const [p, q] of [
				[x, y],
				[y, x]
			]) {
				const only = minus(p.vlans.tagged, q.vlans.tagged);
				if (only.length) {
					reasons.push(`${p.label} carries ${list(only)}, ${q.label} does not`);
					wrong = true;
				}
			}
		}
	}

	// What crosses: tagged VLANs both switch ends carry, plus the native one
	// where the plan declares a native (an access port) and the port sends it.
	const carried = (e: VlanEnd) => [
		...e.vlans.tagged,
		...(declared?.native !== undefined && e.vlans.native !== undefined && e.vlans.untagged.includes(e.vlans.native) ? [e.vlans.native] : [])
	];
	const carries = seen.length ? seen.map(carried).reduce((s, c) => s.filter((x) => c.includes(x))) : [...want, ...(declared?.native !== undefined ? [declared.native] : [])];

	for (const e of [a, b]) {
		if (!e.device) reasons.push(`${e.label} is outside the model`);
		else if (!e.switchPort) reasons.push(`${e.label} tags in its OS: the plan's word`);
		else if (!e.vlans.reported) reasons.push(`${e.label}: no VLANs reported${tags[`${e.device.tag}__Online`] === false ? ` (${e.device.tag} offline)` : ''}`);
	}
	if (wrong) return done('contradicted', carries, seen.length > 0);
	if (!seen.length || (!declared && seen.length < 2)) return done('unverified', carries, seen.length > 0);
	reasons.unshift(declared ? `${seen.map((e) => e.label).join(' and ')} as declared: ${declaredText(declared)}` : `both ends carry ${vlanText(seen[0].vlans)}`);
	return done('consistent', [...new Set(carries)].sort((x, y) => x - y), true);
}

export function checkAllVlans(plant: Plant, tags: Record<string, unknown>): VlanCheck[] {
	return plant.topology.links.map((l) => checkVlans(plant, l, tags));
}

export const VLAN_MARK: Record<VlanVerdict, string> = { consistent: '=', contradicted: '✗', unverified: '?', none: '·' };

/** A port faceplate's VLAN rows: what the switch says, the plan, the check. */
export function vlanFacts(check: VlanCheck, near: 'a' | 'b'): { label: string; value: string }[] {
	const e = near === 'a' ? check.a : check.b;
	const rows: { label: string; value: string }[] = [];
	if (e.vlans.reported) rows.push({ label: 'VLANs', value: `native ${e.vlans.native ?? 'none'}${e.vlans.tagged.length ? ` · tagged ${list(e.vlans.tagged)}` : ''}` });
	if (check.declared) rows.push({ label: 'VLANs declared', value: declaredText(check.declared) + (check.declared.evidence ? ` (${check.declared.evidence})` : '') });
	rows.push({ label: 'VLAN check', value: `${VLAN_MARK[check.verdict]} ${check.verdict}` }, ...check.reasons.map((r) => ({ label: '', value: r })));
	return rows;
}

// ── colours ────────────────────────────────────────────────────────────

/** Categorical, clear of the status hues (no red, amber or green), like
 * KIND_COLORS: a VLAN's colour says which, not how. */
export const VLAN_COLORS = ['#3987e5', '#9b6ee0', '#4cc3d9', '#d46aa8', '#c9a86a', '#2fa38f', '#e07b54'];
/** VLAN 1, the default every unconfigured port sits in: quiet. */
export const DEFAULT_VLAN_COLOR = '#8a94a6';

/** Each VLAN's colour, fixed by its place among the site's VLANs (declared
 * first, then any a switch reports that the plan does not). */
export function vlanColors(ids: number[]): Map<number, string> {
	const out = new Map<number, string>();
	let i = 0;
	for (const id of ids) out.set(id, id === 1 ? DEFAULT_VLAN_COLOR : VLAN_COLORS[i++ % VLAN_COLORS.length]);
	return out;
}

/** The site's VLANs: declared, then any a switch reports beyond them, with
 * the best name known (the switch's, else the plan's). */
export function siteVlans(t: Topology, tags: Record<string, unknown>): { id: number; name?: string; declared: boolean; note?: string }[] {
	const out = new Map<number, { id: number; name?: string; declared: boolean; note?: string }>();
	for (const v of t.vlans ?? []) out.set(v.id, { id: v.id, name: v.name, declared: true, note: v.note });
	const seen: { id: number; name?: string }[] = [];
	for (const d of t.devices) if (d.kind === 'switch') for (const r of vlanTable(tags, d.tag).values()) seen.push(r);
	for (const r of seen.sort((x, y) => x.id - y.id)) {
		const v = out.get(r.id);
		if (v) v.name = r.name ?? v.name;
		else out.set(r.id, { id: r.id, name: r.name, declared: false });
	}
	return [...out.values()];
}

// ── domains: the logical view ──────────────────────────────────────────

export interface DomainLink {
	index: number;
	/** The link carries the VLAN (by the check's `carries`). */
	carried: boolean;
	/** The plan says it should. */
	declared: boolean;
	/** Carried, but the ring's protection link holds it blocked. */
	blocked: boolean;
}

export interface VlanDomain {
	id: number;
	name?: string;
	declared: boolean;
	/** Every link that carries the VLAN or is declared to. */
	links: DomainLink[];
	/** Devices (ids; a far end outside the model by name) in the VLAN. */
	devices: string[];
	/** The devices in groups that can reach each other on this VLAN, the
	 * biggest first: more than one is a VLAN split in two. A server with
	 * links into two islands is in both. */
	islands: string[][];
}

const declares = (l: TopoLink, id: number) => !!l.vlans && ((l.vlans.tagged ?? []).includes(id) || l.vlans.native === id);

/**
 * Each VLAN as a layer-2 domain: the links that carry it and the devices
 * they join, in islands. A link with no light (`down` in the cable check)
 * carries nothing. An ERPS ring's protection link (`rpl: true`) is held
 * blocked while every other ring link is carrying, so a VLAN pruned from
 * one ring link does not find its way round: the ring is protection for a
 * failed link, not for a missing VLAN.
 */
export function vlanDomains(plant: Plant, vlanChecks: VlanCheck[], tags: Record<string, unknown>, cables?: { check: LinkCheck }[]): VlanDomain[] {
	const t = plant.topology;
	const lit = (i: number) => cables?.[i]?.check.verdict !== 'down';
	const ringUp = t.links.every((l, i) => l.kind !== 'ring' || l.rpl || lit(i));
	return siteVlans(t, tags).map((v) => {
		const links: DomainLink[] = [];
		t.links.forEach((l, i) => {
			const carried = lit(i) && vlanChecks[i].carries.includes(v.id);
			const declared = declares(l, v.id);
			if (carried || declared) links.push({ index: i, carried, declared, blocked: carried && !!l.rpl && ringUp });
		});
		// Islands: the switches joined by forwarding links (union-find); a
		// server or the site joins every island it has a carrying link into.
		// Only switches bridge: a server's team across two switches does not
		// forward between them, so it can sit in two islands.
		const isSwitch = (id: string) => deviceById(t, id)?.kind === 'switch';
		const parent = new Map<string, string>();
		const find = (x: string): string => {
			if (!parent.has(x)) parent.set(x, x);
			const p = parent.get(x)!;
			if (p === x) return x;
			const r = find(p);
			parent.set(x, r);
			return r;
		};
		const leaves: [string, string][] = [];
		const members = new Set<string>();
		for (const dl of links) {
			if (!dl.carried) continue;
			const l = t.links[dl.index];
			const a = parseEnd(l.a).device;
			const b = parseEnd(l.b).device;
			members.add(a).add(b);
			if (isSwitch(a) && isSwitch(b)) {
				const x = find(a);
				const y = find(b);
				if (!dl.blocked && x !== y) parent.set(x, y);
			} else if (isSwitch(a)) leaves.push([b, find(a)]);
			else if (isSwitch(b)) leaves.push([a, find(b)]);
			else leaves.push([a, a], [b, a]); // no switch between them: an island of their own
		}
		const order = (d: string) => {
			const i = t.devices.findIndex((x) => x.id === d);
			return i < 0 ? t.devices.length : i;
		};
		const groups = new Map<string, Set<string>>();
		const add = (root: string, d: string) => groups.set(root, (groups.get(root) ?? new Set()).add(d));
		for (const sw of parent.keys()) if (isSwitch(sw)) add(find(sw), sw);
		for (const [leaf, at] of leaves) add(find(at), leaf);
		const devices = [...members].sort((p, q) => order(p) - order(q));
		const islands = [...groups.values()].map((g) => [...g].sort((p, q) => order(p) - order(q))).sort((p, q) => q.length - p.length || order(p[0]) - order(q[0]));
		return { id: v.id, name: v.name, declared: v.declared, links, devices, islands };
	});
}

/** The islands a device sits in (0 the biggest); none when not in the VLAN. */
export const islandsOf = (d: VlanDomain, device: string) => d.islands.flatMap((g, i) => (g.includes(device) ? [i] : []));

/** The modelled devices in a VLAN (a picked VLAN in the rack: what to light). */
export function domainDevices(t: Topology, d: VlanDomain): Set<string> {
	return new Set(d.devices.filter((id) => !!deviceById(t, id)));
}

// ── floors: the logical view's layout ─────────────────────────────────

export interface VlanFloor {
	domain: VlanDomain;
	y: number;
	/** The VLAN's devices where the mesh puts them, at the floor's height. */
	nodes: { id: string; kind: string; pos: Vec3 }[];
	/** Its links as arcs on the floor, each with what the domain says of it. */
	edges: { link: DomainLink; points: [Vec3, Vec3, Vec3] }[];
}

/**
 * One floor per VLAN, stacked, the first on top: each the mesh view's
 * layout (switches on the inner ring, servers outside, the site beyond)
 * flattened to the floor, holding only the devices and links of that
 * VLAN — so the same device sits at the same place on every floor, and a
 * device missing from a floor reads as a gap. `spread` (0..1) draws the
 * floors that far apart: 0 is every floor on one plane at the stack's
 * middle (the view opening out of a single layer), 1 the stack.
 */
export function vlanFloors(t: Topology, domains: VlanDomain[], opts: { gap?: number; top?: number; spread?: number } = {}): VlanFloor[] {
	const { gap = 0.42, top = 1.75, spread = 1 } = opts;
	const mid = top - ((domains.length - 1) * gap) / 2;
	const mesh = meshLayout(t);
	const at = new Map(mesh.nodes.map((n) => [n.id, n]));
	return domains.map((domain, i) => {
		const y = mid + (top - i * gap - mid) * spread;
		const flat = (p: Vec3, lift = 0): Vec3 => [p[0], y + lift, p[2]];
		const nodes = domain.devices.flatMap((id) => {
			const n = at.get(id);
			return n ? [{ id, kind: n.kind, pos: flat(n.pos) }] : [];
		});
		const edges = domain.links.map((link) => {
			const e = mesh.edges[link.index];
			return { link, points: [flat(e.points[0]), flat(e.points[1], 0.03), flat(e.points[2])] as [Vec3, Vec3, Vec3] };
		});
		return { domain, y, nodes, edges };
	});
}
