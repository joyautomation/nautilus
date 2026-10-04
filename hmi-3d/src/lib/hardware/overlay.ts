// Overlays (docs/design/spatial-hmi.md §3e): one question asked of every
// part — how hot, is it linked, is it free — answered as a colour, a short
// text and a legend. An overlay is a pure function of the part and the
// frame's tags, so it is testable, and <Server> (and later the AR view)
// only paints what it returns. Parts an overlay has nothing to say about
// are dimmed, so the answer stands out.
import type { ChassisProfile, ServerPart } from './profile.js';
import { capacity, member, partState, portReading, tagFor } from './profile.js';
import type { Palette } from '../palette.js';
import { checkLink, deviceByTag, linkAt, readEnd, VERDICT_MARK, type LinkCheck, type Plant, type TopoLink, type Verdict } from './topology.js';
import { checkVlans, declaredText, readPortVlans, siteVlans, vlanColors, vlanText, type VlanCheck, type VlanVerdict } from './vlan.js';

export interface PartPaint {
	/** Tint for the part (or the outline of an empty position). */
	color?: string;
	/** A short value shown on the part. */
	text?: string;
	/** Nothing to say about this part: draw it faint. */
	dim?: boolean;
}

export interface LegendItem {
	color?: string;
	label: string;
}

/** The colours an overlay may use, from the theme (palette.ts). */
export interface OverlayColors {
	/** Sequential ramp, low → high, for magnitude within limits. */
	ramp: string[];
	good: string;
	warning: string;
	critical: string;
	/** Present but idle: an unlinked port. */
	neutral: string;
	/** Highlight with no status meaning: a free slot. */
	accent: string;
}

export interface OverlayContext {
	node: string;
	profile: ChassisProfile;
	tags: Record<string, unknown>;
	colors: OverlayColors;
}

export interface Overlay {
	id: string;
	name: string;
	/** A line under the legend: what the colour means, in words. */
	caption: string;
	paint(part: ServerPart, value: unknown, ctx: OverlayContext): PartPaint;
	legend(ctx: OverlayContext): LegendItem[];
	/** Also draw the profile's standalone sensors (inlet, VRM…). */
	sensors?: boolean;
	/** Set port labels out from the port face on leader lines, in rows,
	 * instead of on the parts: a cable's far end is too long to sit on a
	 * cage 16 mm wide. */
	fanOut?: boolean;
}

/**
 * Where fanned-out port labels go: straight out from each port's face, in
 * rows so labels closer than `gap` along x never share one. Ports whose
 * labels go up (a panel's top row) and down (its lower row) fill their own
 * rows. Each row steps mostly away from the ports (up, or down) and only a
 * little out from the face: from an elevated view, out from the face moves
 * a label down the screen, so equal steps would cancel and stack two rows
 * on one spot. `baseY` is where a label's rows start from instead of its
 * port (a panel's top or bottom row), so labels going up and down from
 * ports at one height never meet. Returns each label's position and the
 * port mouth its leader starts from.
 */
export function fanOutLabels(items: { id: string; at: [number, number, number]; out: 1 | -1; up?: 1 | -1; baseY?: number }[], gap = 0.07, rows = 6, first = 0.025, step = 0.01, rise = 0.028): Map<string, { from: [number, number, number]; to: [number, number, number] }> {
	const res = new Map<string, { from: [number, number, number]; to: [number, number, number] }>();
	const lastBy = new Map<number, number[]>();
	for (const it of [...items].sort((a, b) => a.at[0] - b.at[0])) {
		const up = it.up ?? 1;
		const last = lastBy.get(up) ?? [];
		lastBy.set(up, last);
		let r = last.findIndex((x) => it.at[0] - x >= gap);
		if (r < 0) r = last.length < rows ? last.length : last.indexOf(Math.min(...last));
		last[r] = it.at[0];
		res.set(it.id, { from: it.at, to: [it.at[0], (it.baseY ?? it.at[1]) + up * (0.006 + r * rise), it.at[2] + it.out * (first + r * step)] });
	}
	return res;
}

/** The validated dark-surface ordinal ramp (blue 600 → 200), low → high. */
export const HEAT_RAMP = ['#184f95', '#256abf', '#3987e5', '#6da7ec', '#9ec5f4'];

/** An overlay's colours from the theme's palette — one place, so the
 * parts and the legend can never disagree. */
export function overlayColors(p: Palette): OverlayColors {
	return { ramp: HEAT_RAMP, good: p.running, warning: p.priority.medium, critical: p.priority.critical, neutral: '#6b6b68', accent: p.hover };
}

const num = (v: unknown): number | undefined => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);
const DIM: PartPaint = { dim: true };

/** The device is offline (`{node}__Online` false): its tags hold their last
 * values, which say nothing about now. An overlay of live readings paints
 * nothing then — never a stale "10G" or "drops 50/s" as if current. */
export const deviceOffline = (ctx: Pick<OverlayContext, 'node' | 'tags'>) => ctx.tags[`${ctx.node}__Online`] === false;

// ── heat ───────────────────────────────────────────────────────────────

/** A part's [warning, critical] °C, from the profile's `limits` by
 * `kind:bus`, then `kind`. */
export function limitsFor(p: ChassisProfile, part: Pick<ServerPart, 'kind' | 'props'>): [number, number] | undefined {
	const l = (p.limits ?? {}) as Record<string, [number, number]>;
	const bus = part.props.bus;
	return (typeof bus === 'string' ? l[`${part.kind}:${bus}`] : undefined) ?? l[part.kind];
}

/** The inlet temperature the headroom is measured from. */
export function inletC(ctx: Pick<OverlayContext, 'tags' | 'node' | 'profile'>): number {
	const fromServer = num(member(ctx.tags[ctx.node], 'InletTempC'));
	if (fromServer !== undefined) return fromServer;
	const inlet = ctx.profile.sensors?.find((s) => s.id === 'inlet');
	const fromSensor = inlet ? num(member(ctx.tags[tagFor(inlet.tag, ctx.node)], 'Value')) : undefined;
	return fromSensor ?? 25;
}

/**
 * One reading against its limits: over critical, over warning, or the
 * share of headroom used — inlet to the warning limit — as a ramp step.
 * Headroom, not °C: a 23 °C drive in a 23 °C room has used none of it.
 */
export function heatPaint(t: number, warn: number, crit: number, inlet: number, c: OverlayColors): PartPaint {
	const deg = `${Math.round(t)}°`;
	if (t >= crit) return { color: c.critical, text: `${deg} CRIT` };
	if (t >= warn) return { color: c.warning, text: `${deg} HIGH` };
	const r = Math.min(Math.max((t - inlet) / Math.max(warn - inlet, 1), 0), 0.9999);
	return { color: c.ramp[Math.floor(r * c.ramp.length)], text: deg };
}

/**
 * A standalone sensor's [warning, critical] from its own setpoints. BMCs
 * often set only the critical one and report the warning as 0: an unset
 * warning is the critical limit, never 0 °C (which would flag every
 * reading HIGH).
 */
export function sensorLimits(v: unknown): [number, number] | undefined {
	const crit = num(member(v, 'HighHighSP'));
	const warn = num(member(v, 'HighSP'));
	if (crit === undefined || crit <= 0) return warn !== undefined && warn > 0 ? [warn, warn] : undefined;
	return [warn !== undefined && warn > 0 && warn < crit ? warn : crit, crit];
}

export const heatOverlay: Overlay = {
	id: 'heat',
	name: 'Heat',
	caption: 'Each part against its own limit: the share of headroom used, from the inlet temperature to the part’s warning limit. No colour between sensors — only measured parts are painted.',
	sensors: true,
	paint(part, value, ctx) {
		if (deviceOffline(ctx)) return DIM;
		const t = num(member(value, 'TempC'));
		const lim = limitsFor(ctx.profile, part);
		// A driver delivers a member it has no binding for as zero; no part
		// inside a running server reads 0 °C, so 0 is "not reported".
		if (t === undefined || t === 0 || !lim) return DIM;
		return heatPaint(t, lim[0], lim[1], inletC(ctx), ctx.colors);
	},
	legend(ctx) {
		const inlet = Math.round(inletC(ctx));
		const n = ctx.colors.ramp.length;
		return [
			...ctx.colors.ramp.map((color, i) => ({
				color,
				label: i === 0 ? `inlet (${inlet}°) +` : i === n - 1 ? `→ ${Math.round(((i + 1) / n) * 100)}% of headroom` : `${Math.round(((i + 1) / n) * 100)}%`
			})),
			{ color: ctx.colors.warning, label: 'over warning (HIGH)' },
			{ color: ctx.colors.critical, label: 'over critical (CRIT)' },
			{ label: 'faint: no temperature reported' }
		];
	}
};

// ── interfaces ─────────────────────────────────────────────────────────

export const interfacesOverlay: Overlay = {
	id: 'interfaces',
	name: 'Interfaces',
	caption: 'Every port by link: green at its rated speed, amber when linked slower than the port can run, grey when unused. A PSU shows its power input.',
	paint(part, value, ctx) {
		const c = ctx.colors;
		if (deviceOffline(ctx)) return DIM;
		if (part.kind === 'port') {
			// A port with a name in the profile says it (BMC, LAN1); the NIC
			// cages, 16 mm apart, keep to the speed alone.
			const name = typeof part.props.short === 'string' ? `${part.props.short} ` : '';
			const said = (p: PartPaint): PartPaint => ({ ...p, text: `${name}${p.text}` });
			if (value === undefined) return said({ color: c.neutral, text: 'not reported', dim: true });
			const link = portReading(value);
			if (link.up !== true) return said({ color: c.neutral, text: 'no link' });
			const speed = link.gbps;
			const rated = num(part.props.ratedGbps);
			// Short: ports sit 16 mm apart. `25G`, below rated `10/25G`.
			const g = (x: number) => (x >= 1 ? `${+x.toFixed(1)}` : `${+x.toFixed(2)}`);
			if (speed === undefined) return said({ color: c.good, text: 'up' });
			if (rated !== undefined && speed < rated) return said({ color: c.warning, text: `${g(speed)}/${rated}G` });
			return said({ color: c.good, text: `${g(speed)}G` });
		}
		if (part.kind === 'psu') {
			if (value === undefined) return DIM;
			return member(value, 'InputOk') === true ? { color: c.good, text: 'AC in' } : { color: c.critical, text: 'no input' };
		}
		return DIM;
	},
	legend(ctx) {
		const c = ctx.colors;
		return [
			{ color: c.good, label: 'linked at rated speed (25G)' },
			{ color: c.warning, label: 'linked below rated speed (10/25G)' },
			{ color: c.neutral, label: 'no link' },
			{ color: c.critical, label: 'PSU without input' }
		];
	}
};

// ── what's free ────────────────────────────────────────────────────────

/**
 * Where each overlay label goes, so neighbours stay readable: parts a few
 * millimetres apart (two ports on a card, a DIMM bank, stacked bays) would
 * otherwise print on top of each other. The same text close by is said
 * once; a different text steps down below its neighbour.
 */
export function placeLabels(items: { id: string; at: [number, number, number]; text: string }[], near = 0.03, step = 0.011): Map<string, [number, number, number]> {
	const out = new Map<string, [number, number, number]>();
	const placed: { at: [number, number, number]; text: string }[] = [];
	for (const it of items) {
		const at: [number, number, number] = [...it.at];
		const close = (q: { at: [number, number, number] }) => Math.hypot(q.at[0] - at[0], q.at[2] - at[2]) < near && Math.abs(q.at[1] - at[1]) < step * 0.9;
		if (placed.some((q) => q.text === it.text && Math.hypot(q.at[0] - it.at[0], q.at[2] - it.at[2]) < near && Math.abs(q.at[1] - it.at[1]) < near)) continue;
		for (let i = 0; i < 4 && placed.some(close); i++) at[1] -= step;
		placed.push({ at, text: it.text });
		out.set(it.id, at);
	}
	return out;
}

/** The DIMM locators to fill next: the smallest population step in the
 * profile's `population.dimm` that contains every fitted slot and adds one. */
export function nextDimms(p: ChassisProfile, fitted: Set<string>): Set<string> {
	const steps = ((p.population as { dimm?: string[][] } | undefined)?.dimm ?? []).map((s) => new Set(s));
	for (const s of steps) {
		if (s.size <= fitted.size) continue;
		if ([...fitted].every((l) => s.has(l))) return new Set([...s].filter((l) => !fitted.has(l)));
	}
	return new Set();
}

export const freeOverlay: Overlay = {
	id: 'free',
	name: 'Free',
	caption: 'Where a part can go: empty bays, slots and sockets; the DIMMs to fill next follow the board manual’s population order.',
	paint(part, value, ctx) {
		const c = ctx.colors;
		const empty = !part.static && (partState(part, value, true) === 'unbound' || partState(part, value, true) === 'missing');
		if (part.kind === 'psu' && value !== undefined) {
			if (deviceOffline(ctx)) return DIM;
			const out = num(member(value, 'OutputW'));
			const cap = num(member(value, 'CapacityW'));
			return out !== undefined && cap !== undefined ? { text: `${Math.round(out)}/${cap} W` } : DIM;
		}
		if (!empty || part.kind === 'port') return DIM;
		if (part.kind === 'dimm') {
			const fitted = new Set(
				ctx.profile.parts
					.filter((q) => q.kind === 'dimm')
					.filter((q) => ctx.tags[tagFor(ctx.profile.bindings[q.id] ?? '', ctx.node)] !== undefined)
					.map((q) => q.id.replace(/^dimm/, ''))
			);
			const loc = part.partId.replace(/^dimm/, '');
			return nextDimms(ctx.profile, fitted).has(loc) ? { color: c.accent, text: `${loc} next` } : { color: c.neutral };
		}
		// Name the position, so stacked bays each get their own label.
		const bay = /bay\s*(\d+)/i.exec(part.slot)?.[1];
		const bus = typeof part.props.bus === 'string' ? ` ${part.props.bus.toUpperCase()}` : '';
		return { color: c.accent, text: bay !== undefined ? `bay ${bay}${bus}` : `free${bus}` };
	},
	legend(ctx) {
		const c = ctx.colors;
		return [
			{ color: c.accent, label: 'free bay or slot (DIMM: fill next)' },
			{ color: c.neutral, label: 'free DIMM, later in the order' },
			{ label: 'PSU: load / capacity, W' }
		];
	}
};

// ── identify ───────────────────────────────────────────────────────────

/** One colour per kind of part: categorical, and clear of the status hues
 * (no red, amber or green), since this overlay says what, not how. */
export const KIND_COLORS: Record<string, string> = {
	drive: '#3987e5',
	dimm: '#9b6ee0',
	cpu: '#4cc3d9',
	'pcie-card': '#2fa38f',
	fan: '#8a94a6',
	psu: '#c9a86a',
	port: '#d46aa8'
};
const KIND_NAMES: Record<string, string> = { drive: 'drive', dimm: 'memory', cpu: 'CPU', 'pcie-card': 'expansion card', fan: 'fan', psu: 'power supply', port: 'network port' };

/** What a part is, in a few words: where it sits and what the inventory
 * (or the profile, for a fitted part no driver reports) says it is. */
export function identify(part: ServerPart, value: unknown): string | undefined {
	const v = value ?? part.static;
	const s = (k: string) => {
		const x = member(v, k);
		return typeof x === 'string' && x ? x : undefined;
	};
	const n = (k: string) => num(member(v, k));
	switch (part.kind) {
		case 'drive': {
			const bay = /bay\s*(\d+)/i.exec(part.slot)?.[1];
			const where = part.props.form === 'm2' ? 'M.2' : bay !== undefined ? `bay ${bay}` : part.slot;
			return [where, [capacity(member(v, 'CapacityGB')), s('Protocol')].filter(Boolean).join(' ')].filter(Boolean).join(' · ');
		}
		case 'dimm': {
			const cap = capacity(member(v, 'CapacityGB'));
			return `${part.partId.replace(/^dimm/, '')}${cap ? ` · ${cap}` : ''}`;
		}
		case 'cpu':
			return [s('Model') ?? 'CPU', n('Cores') !== undefined ? `${n('Cores')}c` : undefined].filter(Boolean).join(' · ');
		case 'pcie-card':
			return s('Model') ?? s('Name') ?? part.slot;
		case 'fan':
			return part.slot;
		case 'psu':
			return `${part.slot}${n('CapacityW') !== undefined ? ` · ${n('CapacityW')} W` : ''}`;
		case 'port':
			return typeof part.props.port === 'string' ? part.props.port : typeof part.props.short === 'string' ? part.props.short : (s('Name') ?? part.partId);
	}
}

export const identifyOverlay: Overlay = {
	id: 'identify',
	name: 'Identify',
	fanOut: true,
	caption: 'What each part is and where it sits, coloured by kind. Empty positions are left faint: the free overlay names them.',
	paint(part, value, ctx) {
		const state = partState(part, value, true);
		if (state === 'unbound' || state === 'missing' || state === 'absent') return DIM;
		const text = identify(part, value);
		return text ? { color: KIND_COLORS[part.kind], text } : DIM;
	},
	legend() {
		return Object.entries(KIND_COLORS).map(([k, color]) => ({ color, label: KIND_NAMES[k] }));
	}
};

// ── cables ─────────────────────────────────────────────────────────────

/** A verdict's colour: confirmed and consistent are both fine, told apart
 * by hue (green: an end named the other; blue: both up, nothing named). */
export function verdictColor(v: Verdict, c: OverlayColors): string {
	return { confirmed: c.good, consistent: c.ramp[2], contradicted: c.critical, down: c.warning, unverified: c.neutral }[v];
}

/** The link on one of this device's ports, checked — for the faceplate. */
export function linkOnPort(plant: Plant, node: string, partId: string, profile: OverlayContext['profile'], tags: Record<string, unknown>): { check: LinkCheck; near: 'a' | 'b'; link: TopoLink } | undefined {
	const device = deviceByTag(plant.topology, node);
	const part = profile.parts.find((q) => q.id === partId);
	const at = device && part ? linkAt(plant.topology, device.id, part) : undefined;
	return at ? { check: checkLink(plant, at.link, tags), near: at.near, link: at.link } : undefined;
}

/**
 * Every port labelled with its far end (`sw1 te0/25`, `node2 slot3 p1`)
 * from the site's topology, coloured by the live check of that declared
 * link (topology.ts). A port with link that the plan does not mention is
 * called out: a cable nobody wrote down.
 */
export function cablesOverlay(plant: Plant): Overlay {
	return {
		id: 'cables',
		name: 'Cables',
		fanOut: true,
		caption: 'Each port’s far end from the site topology, checked live: ✓ an end names the other (LLDP / MAC), = both ends up at the same speed, ✗ the ends disagree, ↓ no end that reports has link, ? an end is not reported (a far end outside the model, like the site, is taken on the end in it).',
		paint(part, value, ctx) {
			if (part.kind !== 'port') return DIM;
			const at = linkOnPort(plant, ctx.node, part.partId, ctx.profile, ctx.tags);
			if (!at) return !deviceOffline(ctx) && readEnd(value).up ? { color: ctx.colors.warning, text: '! not in plan' } : DIM;
			const far = at.near === 'a' ? at.check.b : at.check.a;
			return { color: verdictColor(at.check.verdict, ctx.colors), text: `${VERDICT_MARK[at.check.verdict]} ${far.label}` };
		},
		legend(ctx) {
			const c = ctx.colors;
			return [
				{ color: verdictColor('confirmed', c), label: '✓ confirmed: an end names the other' },
				{ color: verdictColor('consistent', c), label: '= consistent: both up, same speed' },
				{ color: verdictColor('contradicted', c), label: '✗ contradicted' },
				{ color: verdictColor('down', c), label: '↓ declared, no link' },
				{ color: verdictColor('unverified', c), label: '? an end not reported' },
				{ color: c.warning, label: '! linked, not in the plan' }
			];
		}
	};
}

// ── VLANs ──────────────────────────────────────────────────────────────

/** A VLAN verdict's colour, the cable verdicts' hues: = blue, ✗ red, ? grey. */
export function vlanVerdictColor(v: VlanVerdict, c: OverlayColors): string {
	return { consistent: c.ramp[2], contradicted: c.critical, unverified: c.neutral, none: c.neutral }[v];
}

/** A port as a label names it: its plan name (te0/25), else its short name. */
const portLabel = (part: ServerPart) => (typeof part.props.port === 'string' ? part.props.port : typeof part.props.short === 'string' ? part.props.short : part.partId);

/** The VLAN check of the link on one of this device's ports. */
export function vlansOnPort(plant: Plant, node: string, partId: string, profile: OverlayContext['profile'], tags: Record<string, unknown>): { check: VlanCheck; near: 'a' | 'b'; link: TopoLink } | undefined {
	const device = deviceByTag(plant.topology, node);
	const part = profile.parts.find((q) => q.id === partId);
	const at = device && part ? linkAt(plant.topology, device.id, part) : undefined;
	return at ? { check: checkVlans(plant, at.link, tags), near: at.near, link: at.link } : undefined;
}

/**
 * Every switch port by its VLANs as the switch reports them — coloured by
 * its native VLAN, labelled `20 · T 21,22` (native, then tagged) — and red
 * where the port's link carries other than the topology declares (vlan.ts).
 * A server port says what the plan puts on it: hosts tag in their OS,
 * which a BMC cannot see. `pick` lights one VLAN: the ports carrying it in
 * its colour, the rest faint.
 */
export function vlanOverlay(plant: Plant, pick?: number): Overlay {
	const colorsFor = (tags: Record<string, unknown>) => vlanColors(siteVlans(plant.topology, tags).map((v) => v.id));
	const paintPort = (part: ServerPart, ctx: OverlayContext): PartPaint => {
		const device = deviceByTag(plant.topology, ctx.node);
		const at = vlansOnPort(plant, ctx.node, part.partId, ctx.profile, ctx.tags);
		const vc = colorsFor(ctx.tags);
		if (device?.kind !== 'switch') {
			const d = at?.check.declared;
			if (!d) return DIM;
			const ids = [...(d.native !== undefined ? [d.native] : []), ...(d.tagged ?? [])];
			if (pick !== undefined) return ids.includes(pick) ? { color: vc.get(pick), text: `${pick} (plan)` } : DIM;
			return { color: vc.get(ids[0]), text: `${declaredText(d)} (plan)` };
		}
		const pv = readPortVlans(ctx.tags, ctx.node, part.tag);
		if (!pv.reported) return DIM;
		const text = vlanText(pv);
		if (pick !== undefined) {
			const carries = pv.tagged.includes(pick) || (pv.native === pick && pv.untagged.includes(pick));
			return carries ? { color: vc.get(pick), text } : DIM;
		}
		if (at?.check.verdict === 'contradicted') return { color: ctx.colors.critical, text: `✗ ${text}` };
		// A port in the default VLAN alone, with no link the plan names:
		// unconfigured, nothing to say.
		if (!at && !pv.tagged.length && (pv.native ?? 1) === 1) return DIM;
		return { color: pv.native !== undefined ? vc.get(pv.native) : ctx.colors.neutral, text };
	};
	return {
		id: 'vlans',
		name: 'VLANs',
		caption:
			pick !== undefined
				? `The ports that carry VLAN ${pick}, tagged or as their native VLAN.`
				: 'Each switch port’s VLANs as the switch reports them: native, then T and the tagged ones; coloured by the native VLAN. ✗ red: the link carries other than the site topology declares. A server port shows the plan (hosts tag in their OS).',
		// One label per port on a leader, named: neighbours often carry the
		// same VLANs, and a shared label would not say which port it means.
		fanOut: true,
		paint(part, value, ctx) {
			if (part.kind !== 'port') return DIM;
			const p = paintPort(part, ctx);
			return p.text ? { ...p, text: `${portLabel(part)}: ${p.text}` } : p;
		},
		legend(ctx) {
			const vc = colorsFor(ctx.tags);
			return [
				...siteVlans(plant.topology, ctx.tags).map((v) => ({ color: vc.get(v.id), label: `${v.id} ${v.name ?? ''}${v.declared ? '' : ' (not in the plan)'}` })),
				...(pick === undefined ? [{ color: ctx.colors.critical, label: '✗ carries other than declared' }, { label: 'faint: VLAN 1 only, no planned link' }] : [])
			];
		}
	};
}

// ── traffic ────────────────────────────────────────────────────────────

/** Load bands (% of line rate, the busier direction): ramp steps 0..3 end at
 * these, step 4 beyond. Bands, not a straight %: a working port at 2% and
 * an idle one at 0.001% must not look the same. */
export const LOAD_BANDS = [1, 10, 40, 70];
/** Over this, a port with no drops yet is close to saturating. */
export const LOAD_WARN = 90;

/** `26k`, `340M`, `3.2G`: bits per second, short enough for a port label. */
export function bitsText(v: number): string {
	const f = (x: number) => (x >= 10 ? Math.round(x) : +x.toFixed(1));
	// Pick the unit after rounding, so 999.9M reads 1G, not 1000M.
	for (const [k, u] of [[1e9, 'G'], [1e6, 'M'], [1e3, 'k']] as const) if (f(v / k) >= 1 && (u === 'G' || f(v / k) < 1000)) return `${f(v / k)}${u}`;
	return `${Math.round(v)}`;
}

/** A port's traffic from its SwitchPort: in and out, the load as a share of
 * its line rate (the busier direction) and the drops per second. */
export interface PortTraffic {
	up: boolean;
	inBps: number;
	outBps: number;
	load: number;
	drops: number;
	broadcastPps: number;
	multicastPps: number;
}

export function portTraffic(v: unknown): PortTraffic | undefined {
	if (v === undefined || v === null) return undefined;
	const { up } = portReading(v);
	const inBps = num(member(v, 'InBps')) ?? 0;
	const outBps = num(member(v, 'OutBps')) ?? 0;
	const mbps = num(member(v, 'SpeedMbps')) ?? 0;
	const load = mbps > 0 ? (100 * Math.max(inBps, outBps)) / (mbps * 1e6) : 0;
	return { up: up === true, inBps, outBps, load, drops: num(member(v, 'DiscardRate')) ?? 0, broadcastPps: num(member(v, 'InBroadcastPps')) ?? 0, multicastPps: num(member(v, 'InMulticastPps')) ?? 0 };
}

/** A load's colour: dropping is red, nearly full amber, else its band. */
export function trafficColor(t: PortTraffic, c: OverlayColors): string {
	if (t.drops >= 1) return c.critical;
	if (t.load >= LOAD_WARN) return c.warning;
	const i = LOAD_BANDS.findIndex((b) => t.load < b);
	return c.ramp[i < 0 ? c.ramp.length - 1 : i];
}

const pct = (x: number) => (x >= 10 ? `${Math.round(x)}%` : x >= 1 ? `${x.toFixed(1)}%` : x > 0 ? '<1%' : '0%');

/** `↓3.2G ↑410M · 32%`, then `· drops 120/s` when dropping. `↓` is what
 * arrives at the port, `↑` what it sends. */
export function trafficText(t: PortTraffic): string {
	return `↓${bitsText(t.inBps)} ↑${bitsText(t.outBps)} · ${pct(t.load)}${t.drops >= 1 ? ` · drops ${Math.round(t.drops)}/s` : ''}`;
}

/** A server port's traffic, read off the switch port its cable reaches —
 * a BMC sees link, not traffic — turned round: what the switch sends, the
 * server receives. */
export function peerTraffic(plant: Plant, node: string, partId: string, profile: OverlayContext['profile'], tags: Record<string, unknown>): { traffic: PortTraffic; peer: string } | undefined {
	const at = linkOnPort(plant, node, partId, profile, tags);
	const far = at && (at.near === 'a' ? at.check.b : at.check.a);
	if (!far?.tag || far.device?.kind !== 'switch' || tags[`${far.device.tag}__Online`] === false) return undefined;
	const t = portTraffic(tags[far.tag]);
	return t ? { traffic: { ...t, inBps: t.outBps, outBps: t.inBps }, peer: far.label } : undefined;
}

/**
 * Every port by its traffic: in and out, the load on its line (banded,
 * blue light → heavy), red where it drops frames — saturation's evidence,
 * which a 5 s average can hide — amber close to full. A server port shows
 * its switch peer's numbers, turned round. Fans out like cables.
 */
export function trafficOverlay(plant: Plant): Overlay {
	return {
		id: 'traffic',
		name: 'Traffic',
		fanOut: true,
		caption: `Each port's traffic, ↓ in and ↑ out, and its load: the busier direction as a share of its line rate, in bands (<1%, <10%, <40%, <70%, more). Red: dropping frames, the line saturated. Amber: over ${LOAD_WARN}%. A server port shows its switch peer's numbers (its BMC sees no traffic).`,
		paint(part, value, ctx) {
			if (part.kind !== 'port') return DIM;
			const name = portLabel(part);
			const device = deviceByTag(plant.topology, ctx.node);
			if (device?.kind !== 'switch') {
				const p = peerTraffic(plant, ctx.node, part.partId, ctx.profile, ctx.tags);
				if (!p || !p.traffic.up) return DIM;
				return { color: trafficColor(p.traffic, ctx.colors), text: `${name}: ${trafficText(p.traffic)} (${p.peer})` };
			}
			if (deviceOffline(ctx)) return DIM;
			const t = portTraffic(value);
			if (!t || !t.up) return DIM;
			return { color: trafficColor(t, ctx.colors), text: `${name}: ${trafficText(t)}` };
		},
		legend(ctx) {
			const c = ctx.colors;
			const edges = [0, ...LOAD_BANDS];
			return [
				...c.ramp.map((color, i) => ({ color, label: i < LOAD_BANDS.length ? `${edges[i]}–${edges[i + 1]}% of line rate` : `over ${edges[i]}%` })),
				{ color: c.warning, label: `over ${LOAD_WARN}%: close to full` },
				{ color: c.critical, label: 'dropping frames: saturated' },
				{ label: 'faint: no link' }
			];
		}
	};
}

/** An overlay's colours without its labels: for many devices at once (a
 * rack of switches 1U apart), where every port's label would pile onto the
 * next device's. The legend, or a focus, says the rest. */
export function mute(o: Overlay): Overlay {
	return { ...o, fanOut: false, paint: (part, value, ctx) => ({ ...o.paint(part, value, ctx), text: undefined }) };
}

export const OVERLAYS: Overlay[] = [identifyOverlay, heatOverlay, interfacesOverlay, freeOverlay];
