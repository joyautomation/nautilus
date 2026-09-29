// Overlays (docs/design/spatial-hmi.md §3e): one question asked of every
// part — how hot, is it linked, is it free — answered as a colour, a short
// text and a legend. An overlay is a pure function of the part and the
// frame's tags, so it is testable, and <Server> (and later the AR view)
// only paints what it returns. Parts an overlay has nothing to say about
// are dimmed, so the answer stands out.
import type { ChassisProfile, ServerPart } from './profile.js';
import { member, partState, tagFor } from './profile.js';
import type { Palette } from '../palette.js';

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
			if (value === undefined) return { color: c.neutral, text: 'not reported', dim: true };
			if (member(value, 'LinkUp') !== true) return { color: c.neutral, text: 'no link' };
			const speed = num(member(value, 'SpeedGbps'));
			const rated = num(part.props.ratedGbps);
			// Short: ports sit 16 mm apart. `25G`, below rated `10/25G`.
			const g = (x: number) => (x >= 1 ? `${+x.toFixed(1)}` : `${+x.toFixed(2)}`);
			if (speed === undefined) return { color: c.good, text: 'up' };
			if (rated !== undefined && speed < rated) return { color: c.warning, text: `${g(speed)}/${rated}G` };
			return { color: c.good, text: `${g(speed)}G` };
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
			return nextDimms(ctx.profile, fitted).has(loc) ? { color: c.accent, text: 'next' } : { color: c.neutral };
		}
		const bus = part.props.bus;
		return { color: c.accent, text: typeof bus === 'string' ? `free ${bus.toUpperCase()}` : 'free' };
	},
	legend(ctx) {
		const c = ctx.colors;
		return [
			{ color: c.accent, label: 'free (DIMM: fill next)' },
			{ color: c.neutral, label: 'free DIMM, later in the order' },
			{ label: 'PSU: load / capacity, W' }
		];
	}
};

export const OVERLAYS: Overlay[] = [heatOverlay, interfacesOverlay, freeOverlay];
