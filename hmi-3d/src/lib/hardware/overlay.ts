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
 * rows so labels closer than `gap` along x never share one. Returns each
 * label's position and the port mouth its leader starts from.
 */
export function fanOutLabels(items: { id: string; at: [number, number, number]; out: 1 | -1; up?: 1 | -1 }[], gap = 0.07, rows = 4, first = 0.025, step = 0.022): Map<string, { from: [number, number, number]; to: [number, number, number] }> {
	const res = new Map<string, { from: [number, number, number]; to: [number, number, number] }>();
	const last: number[] = [];
	for (const it of [...items].sort((a, b) => a.at[0] - b.at[0])) {
		let r = last.findIndex((x) => it.at[0] - x >= gap);
		if (r < 0) r = last.length < rows ? last.length : last.indexOf(Math.min(...last));
		last[r] = it.at[0];
		const d = first + r * step;
		// Rows step out from the face and away from the ports (up, or down
		// for a panel's lower row), so a head-on view separates them too.
		res.set(it.id, { from: it.at, to: [it.at[0], it.at[1] + (it.up ?? 1) * (0.006 + r * 0.013), it.at[2] + it.out * d] });
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
		const t = num(member(value, 'TempC'));
		const lim = limitsFor(ctx.profile, part);
		if (t === undefined || !lim) return DIM;
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
		caption: 'Each port’s far end from the site topology, checked live: ✓ an end names the other (LLDP / MAC), = both ends up at the same speed, ✗ the ends disagree, ↓ no end that reports has link, ? an end is not reported.',
		paint(part, value, ctx) {
			if (part.kind !== 'port') return DIM;
			const at = linkOnPort(plant, ctx.node, part.partId, ctx.profile, ctx.tags);
			if (!at) return readEnd(value).up ? { color: ctx.colors.warning, text: '! not in plan' } : DIM;
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

export const OVERLAYS: Overlay[] = [identifyOverlay, heatOverlay, interfacesOverlay, freeOverlay];
