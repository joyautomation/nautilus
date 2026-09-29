// Chassis profiles (docs/design/spatial-hmi.md §3e): a server's geometry is
// data, its contents are tags. A profile gives every bay, slot, socket, fan
// and PSU position of one chassis model in millimetres; crossing it with the
// node's tags gives the parts that are actually there, so populated and
// empty bays are right by construction. Pure: no Svelte, no three, so the
// unit tests bundle it and the AR layer can reuse it.
import type { Vec3 } from '../scene.js';

/** The part kinds a profile places; each has a component and a UDT. */
export const PART_KINDS = ['drive', 'dimm', 'pcie-card', 'fan', 'psu', 'cpu', 'port'] as const;
export type PartKind = (typeof PART_KINDS)[number];

export interface ProfilePart {
	/** Unique in the profile; the binding key, and the node id's suffix. */
	id: string;
	kind: PartKind;
	/** What the chassis calls the position ("Front bay 0 (NVMe)", "DIMMA1"). */
	slot: string;
	/** Centre, mm, in the profile's frame. */
	pos: Vec3;
	/** Extents [x, y, z], mm. */
	size: Vec3;
	/** Degrees, applied x, y, z. */
	rot?: Vec3;
	/** Where the part moves in the exploded view, mm. */
	explode?: Vec3;
	/** A value to show when no tag is bound: something the profile knows is
	 * fitted but no driver reports yet. Rendered as unverified, never as
	 * healthy. */
	static?: Record<string, unknown>;
	/** Anything else the part's component understands (bus, form, lanes…). */
	[prop: string]: unknown;
}

export interface ProfileAnchor {
	/** mm, in the profile's frame. */
	pos: Vec3;
	/** The label's outward normal and its "up" (the top edge of the code). */
	normal: Vec3;
	up: Vec3;
	/** Printed edge length of the code, mm. */
	size: number;
	/** What the code encodes: `url` (the asset page, readable by any
	 * camera app) or a short text with `{node}` — `NAUT:{node}/L` — which
	 * fits the smallest QR version, for codes too small for a URL. */
	payload?: string;
	note?: string;
}

export interface ChassisProfile {
	profile: string;
	name: string;
	units: 'mm';
	frame?: string;
	/** Outer [width, height, depth], mm. */
	size: Vec3;
	/** `frontCover`: depth of the fixed top cover over the drive cage, mm;
	 * the removable lid is the rest. */
	shell?: { wall?: number; lid?: number; floor?: number; bezel?: number; frontCover?: number; color?: string; metalness?: number };
	boardPlate?: { pos: Vec3; size: Vec3 };
	/** Fixed internals drawn as plain boxes (a power distribution cage). */
	fixtures?: { id: string; name?: string; pos: Vec3; size: Vec3 }[];
	backplane?: { pos: Vec3; size: Vec3 };
	explodeLid?: Vec3;
	/** Registration points for AR: where each printed code sits. Several
	 * codes far apart hold a steadier pose than one large one. */
	anchors?: Record<string, ProfileAnchor>;
	/** The contract each part kind reads: the UDT and its members. */
	types?: Record<string, { type: string; members: string[] }>;
	/** Part id (or `chassis`) → tag name, `{node}` standing for the node's
	 * tag prefix: the agreed names the drivers publish. */
	bindings: Record<string, string>;
	parts: ProfilePart[];
	/** [warning, critical] °C by `kind:bus` or `kind` — the heat overlay's limits. */
	limits?: Record<string, [number, number]>;
	/** Standalone temperature sensors (TempSensor tags) and where they sit, mm. */
	sensors?: { id: string; name: string; tag: string; pos: Vec3 }[];
	/** Population order, e.g. `dimm`: the vendor's sets, smallest first. */
	population?: Record<string, string[][]>;
	[key: string]: unknown;
}

/** A profile part placed for one node: metres, its tag resolved. */
export interface ServerPart {
	/** The scene node id: `{node}/{part id}`. */
	id: string;
	partId: string;
	kind: PartKind;
	slot: string;
	pos: Vec3;
	size: Vec3;
	rot?: Vec3;
	explode: Vec3;
	tag?: string;
	static?: Record<string, unknown>;
	props: Record<string, unknown>;
}

const MM = 0.001;
const m = (v: Vec3): Vec3 => [v[0] * MM, v[1] * MM, v[2] * MM];
const RESERVED = new Set(['id', 'kind', 'slot', 'pos', 'size', 'rot', 'explode', 'static']);

/** `{node}` in a binding → the node's prefix. */
export function tagFor(template: string, node: string): string {
	return template.replaceAll('{node}', node);
}

function isVec3(v: unknown): v is Vec3 {
	return Array.isArray(v) && v.length === 3 && v.every((x) => typeof x === 'number' && Number.isFinite(x));
}

/** Structural errors, with JSON-pointer paths, the way validateScene reports. */
export function validateProfile(p: unknown): { path: string; message: string }[] {
	const errs: { path: string; message: string }[] = [];
	const e = (path: string, message: string) => errs.push({ path, message });
	if (!p || typeof p !== 'object') return [{ path: '', message: 'a profile is an object' }];
	const o = p as Partial<ChassisProfile>;
	if (typeof o.profile !== 'string' || !o.profile) e('/profile', 'required: the profile id');
	if (o.units !== 'mm') e('/units', 'profiles are in millimetres: "mm"');
	if (!isVec3(o.size) || o.size.some((x) => x <= 0)) e('/size', '[width, height, depth], mm, all positive');
	if (!Array.isArray(o.parts)) {
		e('/parts', 'required: an array');
		return errs;
	}
	const ids = new Set<string>();
	o.parts.forEach((q, i) => {
		const at = `/parts/${i}`;
		if (!q || typeof q !== 'object') return e(at, 'a part is an object');
		if (typeof q.id !== 'string' || !/^[A-Za-z][A-Za-z0-9_-]*$/.test(q.id)) e(`${at}/id`, 'a name: a letter, then letters, digits, _ or -');
		else if (ids.has(q.id) || q.id === 'chassis') e(`${at}/id`, `"${q.id}" is used twice (or is the reserved "chassis")`);
		else ids.add(q.id);
		if (!(PART_KINDS as readonly string[]).includes(q.kind)) e(`${at}/kind`, `one of ${PART_KINDS.join(', ')}`);
		if (!isVec3(q.pos)) e(`${at}/pos`, '[x, y, z], mm');
		if (!isVec3(q.size) || q.size.some((x) => x <= 0)) e(`${at}/size`, '[x, y, z] extents, mm, all positive');
		if (q.rot !== undefined && !isVec3(q.rot)) e(`${at}/rot`, '[x, y, z], degrees');
		if (q.explode !== undefined && !isVec3(q.explode)) e(`${at}/explode`, '[x, y, z], mm');
		// A part inside the chassis: its centre within the outer box.
		if (isVec3(q.pos) && isVec3(o.size)) {
			const [w, h, d] = o.size;
			const [x, y, z] = q.pos;
			if (Math.abs(x) > w / 2 || y < 0 || y > h || z > 0 || z < -d) e(`${at}/pos`, `outside the chassis (${w} x ${h} x ${d} mm, front face at z = 0)`);
		}
	});
	for (const [k, t] of Object.entries(o.bindings ?? {})) {
		if (k !== 'chassis' && !ids.has(k)) e(`/bindings/${k}`, `no part "${k}"`);
		if (typeof t !== 'string' || !t.includes('{node}')) e(`/bindings/${k}`, 'a tag name with {node} for the node prefix');
	}
	(o.sensors ?? []).forEach((q, i) => {
		if (!q || typeof q.tag !== 'string' || !q.tag.includes('{node}')) e(`/sensors/${i}/tag`, 'a tag name with {node}');
		if (!q || !isVec3(q.pos)) e(`/sensors/${i}/pos`, '[x, y, z], mm');
	});
	for (const [k, a] of Object.entries(o.anchors ?? {})) {
		if (!a || !isVec3(a.pos) || !isVec3(a.normal) || !isVec3(a.up) || !(a.size > 0)) e(`/anchors/${k}`, 'pos, normal, up (vectors) and size (mm)');
		else if (Math.abs(a.normal[0] * a.up[0] + a.normal[1] * a.up[1] + a.normal[2] * a.up[2]) > 1e-6) e(`/anchors/${k}/up`, 'must be perpendicular to normal');
	}
	return errs;
}

/** The profile's parts for one node: metres, tags resolved, extras as props. */
export function resolveParts(p: ChassisProfile, node: string): ServerPart[] {
	return p.parts.map((q) => {
		const binding = p.bindings[q.id];
		const props: Record<string, unknown> = {};
		for (const [k, v] of Object.entries(q)) if (!RESERVED.has(k)) props[k] = v;
		return {
			id: `${node}/${q.id}`,
			partId: q.id,
			kind: q.kind,
			slot: q.slot,
			pos: m(q.pos),
			size: m(q.size),
			rot: q.rot,
			explode: m(q.explode ?? [0, 0, 0]),
			tag: binding ? tagFor(binding, node) : undefined,
			static: q.static,
			props
		};
	});
}

/** Every tag a server view reads: the chassis and each bound part. */
export function serverTags(p: ChassisProfile, node: string): string[] {
	return Object.values(p.bindings).map((t) => tagFor(t, node));
}

// ── state ──────────────────────────────────────────────────────────────

/**
 * What a part shows, in order of what matters:
 * - `unbound`  no tag in the profile, nothing known: an empty bay or slot
 * - `assumed`  no tag, but the profile says something is fitted (unverified)
 * - `missing`  bound, but the controller does not publish the tag: drivers
 *   publish a slot only when it is fitted, so this reads as empty
 * - `absent`   the driver says Present = false: a bay whose drive was pulled
 * - `stale`    published, quality bad
 * - `critical` / `warning` / `ok` from Health (0 ok, 1 warning, 2 critical,
 *   3 unknown → warning) and Fault / PredictedFailure
 */
export type PartState = 'unbound' | 'assumed' | 'missing' | 'absent' | 'stale' | 'critical' | 'warning' | 'ok';

export function member(v: unknown, k: string): unknown {
	return v && typeof v === 'object' ? (v as Record<string, unknown>)[k] : undefined;
}

/** A port's link from either UDT: a server's NetPort (LinkUp, SpeedGbps)
 * or a switch's SwitchPort (OperUp, SpeedMbps). */
export function portReading(v: unknown): { up?: boolean; gbps?: number } {
	const up = member(v, 'LinkUp') ?? member(v, 'OperUp');
	const g = member(v, 'SpeedGbps');
	const m = member(v, 'SpeedMbps');
	const gbps = typeof g === 'number' ? g : typeof m === 'number' ? m / 1000 : undefined;
	return { up: typeof up === 'boolean' ? up : undefined, gbps };
}

export function partState(part: Pick<ServerPart, 'tag' | 'static'>, value: unknown, good: boolean): PartState {
	if (!part.tag) return part.static ? 'assumed' : 'unbound';
	if (value === undefined || value === null) return 'missing';
	if (member(value, 'Present') === false) return 'absent';
	if (!good) return 'stale';
	const health = member(value, 'Health');
	if (member(value, 'Fault') === true || health === 2) return 'critical';
	if (member(value, 'PredictedFailure') === true || health === 1 || health === 3) return 'warning';
	return 'ok';
}

/** Is there a part to draw in this position? */
export function isFitted(s: PartState): boolean {
	return s === 'ok' || s === 'warning' || s === 'critical' || s === 'stale' || s === 'assumed';
}

export const HEALTH_TEXT: Record<number, string> = { 0: 'OK', 1: 'Warning', 2: 'Critical', 3: 'Unknown' };

const num = (v: unknown): number | undefined => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);
const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined);

/** Bytes the way a label reads them: 1920 GB → 1.92 TB. */
export function capacity(gb: unknown): string | undefined {
	const g = num(gb);
	if (g === undefined) return undefined;
	return g >= 1000 ? `${+(g / 1000).toFixed(2)} TB` : `${Math.round(g)} GB`;
}

/** The label's value text for a part. */
export function partStatus(kind: PartKind, value: unknown, state: PartState): string {
	switch (state) {
		case 'unbound':
			return 'empty';
		case 'assumed':
			return 'unverified';
		case 'missing':
			return 'empty';
		case 'absent':
			return 'ABSENT';
		case 'stale':
			return 'stale';
	}
	const t = num(member(value, 'TempC'));
	const temp = t !== undefined ? ` · ${Math.round(t)} °C` : '';
	const flag = state === 'ok' ? '' : ` · ${state.toUpperCase()}`;
	switch (kind) {
		case 'drive':
			return `${capacity(member(value, 'CapacityGB')) ?? 'drive'}${temp}${flag}`;
		case 'dimm':
			return `${capacity(member(value, 'CapacityGB')) ?? 'DIMM'}${temp}${flag}`;
		case 'fan':
			return `${Math.round(num(member(value, 'RPM')) ?? 0)} rpm${flag}`;
		case 'psu':
			return `${Math.round(num(member(value, 'OutputW')) ?? 0)} W${flag}`;
		case 'cpu':
			return `${str(member(value, 'Model')) ?? 'CPU'}${temp}${flag}`;
		case 'pcie-card':
			return `${str(member(value, 'Model')) ?? 'card'}${temp}${flag}`;
		case 'port': {
			const r = portReading(value);
			if (r.up !== true) return 'no link';
			const g = r.gbps;
			return g === undefined ? 'link up' : g >= 1 ? `${+g.toFixed(1)} Gb/s` : `${Math.round(g * 1000)} Mb/s`;
		}
	}
}

export interface Fact {
	label: string;
	value: string;
}

/** The faceplate's rows for a part: identity first, then condition. */
export function partFacts(kind: PartKind, value: unknown): Fact[] {
	const rows: Fact[] = [];
	const add = (label: string, v: string | undefined) => v !== undefined && rows.push({ label, value: v });
	const s = (k: string) => str(member(value, k));
	const n = (k: string, unit = '', digits = 0) => {
		const x = num(member(value, k));
		return x === undefined ? undefined : `${x.toFixed(digits)}${unit}`;
	};
	const b = (k: string) => {
		const x = member(value, k);
		return typeof x === 'boolean' ? (x ? 'yes' : 'no') : undefined;
	};
	const health = num(member(value, 'Health'));
	switch (kind) {
		case 'drive':
			add('Model', s('Model'));
			add('Capacity', capacity(member(value, 'CapacityGB')));
			add('Protocol', [s('Protocol'), s('MediaType')].filter(Boolean).join(' ') || undefined);
			add('Bay', n('Bay'));
			add('Temperature', n('TempC', ' °C'));
			add('Predicted failure', b('PredictedFailure'));
			break;
		case 'dimm':
			add('Locator', s('Locator'));
			add('Capacity', capacity(member(value, 'CapacityGB')));
			add('Manufacturer', s('Manufacturer'));
			add('Part number', s('PartNumber'));
			add('Temperature', n('TempC', ' °C'));
			break;
		case 'pcie-card':
			add('Model', s('Model'));
			add('Manufacturer', s('Manufacturer'));
			add('Slot', n('Slot'));
			add('Ports', n('Ports'));
			add('Firmware', s('Firmware'));
			add('Temperature', n('TempC', ' °C'));
			break;
		case 'cpu':
			add('Model', s('Model'));
			add('Cores', n('Cores'));
			add('Temperature', n('TempC', ' °C'));
			add('Load', n('Pct', ' %'));
			break;
		case 'fan':
			add('Speed', n('RPM', ' rpm'));
			add('Duty', n('Pct', ' %'));
			add('Present', b('Present'));
			break;
		case 'port': {
			const { up, gbps: g } = portReading(value);
			add('Link', typeof up === 'boolean' ? (up ? 'up' : 'down') : undefined);
			add('Speed', g === undefined ? undefined : g >= 1 ? `${+g.toFixed(1)} Gb/s` : `${Math.round(g * 1000)} Mb/s`);
			add('Name', s('Name'));
			add('MAC', s('MAC'));
			add('Alias', s('Alias'));
			const bps = (k: string) => {
				const x = num(member(value, k));
				return x === undefined ? undefined : x >= 1e9 ? `${(x / 1e9).toFixed(2)} Gb/s` : x >= 1e6 ? `${(x / 1e6).toFixed(1)} Mb/s` : `${(x / 1e3).toFixed(1)} kb/s`;
			};
			add('In', bps('InBps'));
			add('Out', bps('OutBps'));
			add('Errors / s', n('ErrorRate', '', 2));
			break;
		}
		case 'psu':
			add('Output', n('OutputW', ' W'));
			add('Capacity', n('CapacityW', ' W'));
			add('Input', n('InputV', ' V'));
			add('Input OK', b('InputOk'));
			add('Present', b('Present'));
			break;
	}
	add('Health', health !== undefined ? (HEALTH_TEXT[health] ?? String(health)) : undefined);
	add('Fault', b('Fault'));
	add('Serial', s('Serial'));
	return rows;
}

/** The chassis node's rows (the it-drivers Server UDT). */
export function serverFacts(value: unknown): Fact[] {
	const rows: Fact[] = [];
	const add = (label: string, v: string | undefined) => v !== undefined && rows.push({ label, value: v });
	const n = (k: string, unit: string) => {
		const x = num(member(value, k));
		return x === undefined ? undefined : `${Math.round(x)}${unit}`;
	};
	const b = (k: string) => {
		const x = member(value, k);
		return typeof x === 'boolean' ? (x ? 'yes' : 'no') : undefined;
	};
	const health = num(member(value, 'Health'));
	add('Model', str(member(value, 'Model')));
	add('BMC online', b('Online'));
	add('Powered on', b('PowerOn'));
	add('Health', health !== undefined ? (HEALTH_TEXT[health] ?? String(health)) : undefined);
	add('Power', n('PowerW', ' W'));
	add('Inlet', n('InletTempC', ' °C'));
	add('Hottest sensor', n('MaxTempC', ' °C'));
	add('Serial', str(member(value, 'Serial')));
	return rows;
}

/** A switch chassis's rows (the it-drivers Switch UDT). `PortsUp` is
 * the caller's count when it has the ports' tags. */
export function switchFacts(value: unknown, portsUp?: number): Fact[] {
	const rows: Fact[] = [];
	const add = (label: string, v: string | undefined) => v !== undefined && rows.push({ label, value: v });
	const n = (k: string, unit: string) => {
		const x = num(member(value, k));
		return x === undefined ? undefined : `${Math.round(x)}${unit}`;
	};
	const online = member(value, 'Online');
	const total = num(member(value, 'PortsTotal'));
	const up = portsUp ?? num(member(value, 'PortsUp'));
	const uptime = num(member(value, 'UptimeS'));
	add('Model', str(member(value, 'Model')));
	add('Name', str(member(value, 'Name')));
	add('Online', typeof online === 'boolean' ? (online ? 'yes' : 'no') : undefined);
	add('Ports with link', up !== undefined ? `${up}${total !== undefined ? ` of ${total}` : ''}` : undefined);
	add('Uptime', uptime !== undefined ? `${Math.floor(uptime / 86400)} d ${Math.floor((uptime % 86400) / 3600)} h` : undefined);
	add('Temperature', n('TempC', ' °C'));
	add('Fault', typeof member(value, 'Fault') === 'boolean' ? (member(value, 'Fault') ? 'yes' : 'no') : undefined);
	add('Serial', str(member(value, 'Serial')));
	return rows;
}

/** An anchor's payload for one node: `{node}` filled in; `url` stays `url`
 * (the caller knows its HMI's address). */
export function anchorPayload(a: ProfileAnchor, node: string): string {
	return tagFor(a.payload ?? 'url', node);
}

/** Millimetres → the metres a scene uses. */
export const mm = m;
