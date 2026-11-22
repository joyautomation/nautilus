// The asset page (#714 slice 1): one page for one piece of equipment, from a
// descriptor. The descriptor is the equipment's panel (what a mimic's click
// already opens: readings, setpoints, commands, toggles) plus what a page has
// room for: the readings to trend, the equipment key its alarms carry, notes,
// audit and an optional camera. A host (or `naut hmi build`, from a device
// row) writes it as data; `<AssetPage/>` draws it.
//
// Plain TS (no runes, no DOM), so the choices a page makes are tested on node.

import type { AlarmInstance } from './alarms.svelte.js';
import type { MimicPanel, MimicPanelReading } from './mimic.js';

/** What `<AssetPage/>` draws. */
export interface AssetDescriptor {
	/** The asset's id: its page's slug, and the note target (`{kind: 'asset', id}`). */
	id: string;
	/** Heading. Default: the id. */
	title?: string;
	/** A line under it (the device or type, the area). */
	subtitle?: string;
	/** The equipment key its alarms carry (`equipment:` on an alarm). Default: the id; the id matches too. */
	equipment?: string;
	/** The area it is in (the plant model's): the index groups by it. */
	area?: string;
	/** Readings, setpoints, commands and toggles, as the equipment panel has them. */
	panel: MimicPanel;
	/** Tags to trend together. Default: the first few numeric readings. */
	trend?: string[];
	/** An optional live picture: a URL the page shows beside the readings. Left out, no slot. */
	camera?: { url: string; label?: string };
	/** False leaves the notes section out. */
	notes?: boolean;
}

/** How many numeric readings a page trends when it is not told which. */
export const DEFAULT_TREND_MAX = 4;

/** The asset's title. */
export function assetTitle(a: AssetDescriptor): string {
	return a.title || a.panel.title || a.id;
}

/** The numeric-looking readings: those with a unit or a range, or not a true/false (`on`/`off`/`alarm`). */
function numeric(r: MimicPanelReading): boolean {
	if (r.on !== undefined || r.off !== undefined || r.alarm) return false;
	return r.unit !== undefined || r.min !== undefined || r.max !== undefined || r.decimals !== undefined;
}

/** The tags the trend shows: the descriptor's own, else the first numeric readings. */
export function trendTags(a: AssetDescriptor): string[] {
	if (a.trend) return [...new Set(a.trend)];
	return (a.panel.readings ?? []).filter(numeric).map((r) => r.tag).slice(0, DEFAULT_TREND_MAX);
}

/** Every tag the page reads or writes, once each, in panel order. */
export function assetTags(a: AssetDescriptor): string[] {
	const p = a.panel;
	const all = [
		...(p.readings ?? []).map((r) => r.tag),
		...(p.setpoints ?? []).map((s) => s.tag),
		...(p.commands ?? []).flatMap((c) => (c.held ? [c.tag, c.held] : [c.tag])),
		...(p.toggles ?? []).map((t) => t.tag),
		...trendTags(a)
	];
	return [...new Set(all.filter(Boolean))];
}

/** The tags an operator can write from the page: what the audit picker offers. */
export function writableTags(a: AssetDescriptor): string[] {
	const p = a.panel;
	return [...new Set([...(p.setpoints ?? []).map((s) => s.tag), ...(p.commands ?? []).map((c) => c.tag), ...(p.toggles ?? []).map((t) => t.tag)])];
}

const key = (s: string) => s.trim().toLowerCase();

/** The alarms of this equipment, suppressed ones too (they carry why), worst first. */
export function assetAlarms(instances: AlarmInstance[], a: AssetDescriptor): AlarmInstance[] {
	const keys = new Set([key(a.equipment ?? a.id), key(a.id)]);
	const order = ['critical', 'high', 'medium', 'low', 'diagnostic'];
	return instances
		.filter((i) => keys.has(key(i.equipment ?? '')))
		.sort((x, y) => order.indexOf(x.priority) - order.indexOf(y.priority) || x.name.localeCompare(y.name));
}

/** A page slug for an asset id: lowercase letters, digits and dashes. */
export function assetSlug(id: string): string {
	return id
		.trim()
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, '-')
		.replace(/^-+|-+$/g, '');
}

/** The descriptors a page set can be indexed by: unique slugs, in the order given (a later duplicate is dropped). */
export function indexAssets(list: AssetDescriptor[]): { slug: string; asset: AssetDescriptor }[] {
	const seen = new Set<string>();
	const out: { slug: string; asset: AssetDescriptor }[] = [];
	for (const asset of list) {
		const slug = assetSlug(asset.id);
		if (!slug || seen.has(slug)) continue;
		seen.add(slug);
		out.push({ slug, asset });
	}
	return out;
}

/** A descriptor from a mimic's equipment: what its click opens, as a page. */
export function assetFromEquipment(eq: { id: string; label?: string; panel?: MimicPanel | string }, panels: (id: string) => MimicPanel | undefined): AssetDescriptor | null {
	const panel = typeof eq.panel === 'object' ? eq.panel : typeof eq.panel === 'string' ? panels(eq.panel) : undefined;
	if (!panel) return null;
	return { id: eq.id, title: panel.title || eq.label || eq.id, panel };
}

/**
 * A tag's value by path: the tag by that name if the frame has one, else the
 * member of a struct tag (`CONV.Speed` is `Speed` of `CONV`, as a device's
 * tag carries its points). Undefined when there is none: nothing made up.
 */
export function tagValue(tags: Record<string, unknown>, path: string): unknown {
	if (path in tags) return tags[path];
	let cur: unknown = tags;
	for (const part of path.split('.')) {
		if (cur === null || typeof cur !== 'object' || !(part in (cur as Record<string, unknown>))) return undefined;
		cur = (cur as Record<string, unknown>)[part];
	}
	return cur;
}

/** Whether the frame carries the tag or the member at all. */
export function hasTag(tags: Record<string, unknown>, path: string): boolean {
	return tagValue(tags, path) !== undefined;
}

/** The assets by area, areas in the order first seen and the unplaced last; each group in the order given. */
export function assetGroups(list: AssetDescriptor[]): { area: string; assets: AssetDescriptor[] }[] {
	const groups = new Map<string, AssetDescriptor[]>();
	for (const a of list) {
		const k = a.area ?? '';
		groups.set(k, [...(groups.get(k) ?? []), a]);
	}
	const named = [...groups].filter(([k]) => k !== '');
	const none = groups.get('');
	return [...named, ...(none ? ([['', none]] as [string, AssetDescriptor[]][]) : [])].map(([area, assets]) => ({ area, assets }));
}

/** A piece of the plant model's area, for the area page. */
export interface AreaDescriptor {
	id: string;
	label?: string;
	/** The asset ids in the area, in the plant's order. */
	assets: string[];
	/** The route of the area's process scheme, when it has one. */
	scheme?: string;
}

/** One row of an area page. */
export interface AreaRow {
	asset: AssetDescriptor;
	/** Alarms standing on it: unacknowledged, active in all, and hidden by suppression. */
	unacked: number;
	active: number;
	suppressed: number;
	/** The worst priority standing (not counting suppressed ones), or ''. */
	worst: string;
}

const PRIORITIES = ['critical', 'high', 'medium', 'low', 'diagnostic'];

/** The area's rows: its assets in order, each with the alarms standing on it. Unknown asset ids are left out. */
export function areaRows(area: AreaDescriptor, assets: AssetDescriptor[], instances: AlarmInstance[]): AreaRow[] {
	const byId = new Map(assets.map((a) => [key(a.id), a]));
	const rows: AreaRow[] = [];
	for (const id of area.assets) {
		const asset = byId.get(key(id));
		if (!asset) continue;
		const mine = assetAlarms(instances, asset);
		const standing = mine.filter((a) => a.state !== 'suppressed' && a.state !== 'normal');
		const rank = (p: string) => (PRIORITIES.includes(p) ? PRIORITIES.indexOf(p) : 98);
		const worst = standing.reduce<string>((w, a) => (rank(a.priority) < (w ? rank(w) : 99) ? a.priority : w), '');
		rows.push({
			asset,
			unacked: standing.filter((a) => a.state === 'unack-active' || a.state === 'unack-rtn').length,
			active: standing.length,
			suppressed: mine.filter((a) => a.state === 'suppressed').length,
			worst
		});
	}
	return rows;
}
