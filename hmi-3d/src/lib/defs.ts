// The registry's pure half (docs/design/spatial-hmi.md §4, §3d): the
// kind definition, what a component file exports as `kind`, how the app's
// modules become component kinds, how an assembly's box is found, and how
// a document's kinds are laid on a registry. No Svelte component is
// imported here, so the unit tests can bundle it; registry.ts adds the
// built-ins and re-exports all of it.
import type { Component } from 'svelte';
import { kindMembers, type SceneAssembly, type SceneDoc, type SceneError, type SceneKind, type SceneKindEntry, type ScenePort, type Vec3 } from './scene.js';
import { formatStatus } from './drives.js';

/** What every kind's component receives. Static `props` and resolved `bind`
 * props are spread on top (bind wins), so a component declares the extras
 * it understands and ignores the rest. */
export interface NodeProps {
	/** The node's struct tag, whole, or undefined (no tag, or not yet received). */
	value: unknown;
	/** Quality of the node's tag AND of every bound ref's root. */
	good: boolean;
	label: string;
	selected: boolean;
	[prop: string]: unknown;
}

export interface PanelProps {
	value: unknown;
	label: string;
	good: boolean;
}

/** A box in a kind's local space: the halo, the selection outline. */
export interface Box {
	size: Vec3;
	center: Vec3;
}

export interface NodeKindDef {
	/** What draws a node of this kind. Absent on an assembly, whose parts
	 * draw themselves (§3d). */
	component?: Component<NodeProps>;
	/** A data kind (docs/design/spatial-hmi.md §3c): the document's entry,
	 * handed to the component that loads the model and applies the drives. */
	data?: SceneKind;
	/** An assembly (§3d): parts and pipes in the kind's own frame, rendered
	 * by <Node> as parts of the placed node. */
	assembly?: SceneAssembly;
	/** The UDT this kind reads by default, and the members it reads — the
	 * contract `naut check` and `naut scene init` work from. A scene's
	 * `kinds` block re-points `type` for a project whose UDT is named
	 * differently. */
	type?: string;
	members?: string[];
	/** Local-space box for the alarm halo and the selection outline.
	 * `'auto'` on a data kind: the model reports its box once loaded. */
	bounds: { size: Vec3; center: Vec3 } | 'auto';
	/** Where the floating label sits, local space; on a data kind, absent =
	 * the top centre of the bounds. */
	labelAt?: Vec3;
	/** The label's value text. Pure, so it is testable and matches the panel.
	 * `over` is the node's resolved `bind` map (prop -> value), which wins
	 * over the struct's member the way it does for the geometry, so a
	 * flat-tag node's label says what its model shows. */
	status?: (value: unknown, good: boolean, over?: Record<string, unknown>) => string;
	/** Where pipes meet this kind, in its frame (§3g slice 3); a document's
	 * `kinds` entry may add or override them. */
	ports?: Record<string, ScenePort>;
	/** The 2D faceplate the inspector drawer shows; absent = the member table only. */
	panel?: Component<PanelProps>;
	/** A stand-in, not a kind of its own: what an editor draws when a
	 * model kind's file does not load. `registryFor` lays the model over
	 * it in every look — only a real Svelte kind of the same name (a
	 * built-in re-pointed at a model) keeps its primitives in flat and
	 * paper. */
	fallback?: boolean;
}

export type NodeRegistry = Record<string, NodeKindDef>;


/** What a component file exports as `kind` from its `<script module>`
 * (docs/design/spatial-hmi.md §3d): `NodeKindDef` minus the component. */
export type KindMeta = Omit<NodeKindDef, 'component' | 'data' | 'assembly' | 'bounds'> & { bounds?: Box };

const FALLBACK_BOX: Box = { size: [0.3, 0.3, 0.3], center: [0, 0.15, 0] };

/** A registry entry from a component and its `kind` export. */
export function kindOf(component: Component<NodeProps>, meta: KindMeta = {}): NodeKindDef {
	return { ...meta, component, bounds: meta.bounds ?? FALLBACK_BOX };
}

/**
 * The module a component path names, among the keys of an
 * `import.meta.glob` result: `hmi/src/lib/Skid.svelte` (a path from the
 * scene file, §3d) ends with `/src/lib/Skid.svelte` (a key from the app
 * root). The longest key that fits wins, so two `Skid.svelte` in different
 * folders resolve to the one the document actually names.
 */
export function matchModule(path: string, keys: Iterable<string>): string | undefined {
	const p = '/' + path.replace(/^\.?\//, '');
	let best: string | undefined;
	let bestLen = -1;
	for (const k of keys) {
		const key = k.startsWith('/') ? k : '/' + k.replace(/^\.?\//, '');
		if (p.endsWith(key) && key.length > bestLen) {
			best = k;
			bestLen = key.length;
		}
	}
	return best;
}

/**
 * A document with its kind files inlined (§3g slice 2): every `kinds`
 * entry that is a path (`hmi/src/lib/kinds/pump.kind.json`) is replaced by
 * the object that file holds, found among the app's modules the way a
 * component file is (`import.meta.glob` of the app's `*.kind.json`, eager
 * — a JSON module's default export is the object). `sources` says which
 * module each inlined kind came from. Errors carry the document's paths: a
 * path no module matches, a module that is not an object. An entry that
 * is not a path is left as it is; a document with no path entries comes
 * back as it was.
 */
export function inlineKindFiles(
	doc: SceneDoc,
	modules: Record<string, unknown>
): { doc: SceneDoc; errors: SceneError[]; sources: Record<string, string> } {
	const errors: SceneError[] = [];
	const sources: Record<string, string> = {};
	const entries = Object.entries(doc.kinds ?? {});
	if (!entries.some(([, k]) => typeof k === 'string')) return { doc, errors, sources };
	const kinds: Record<string, SceneKindEntry> = {};
	for (const [name, k] of entries) {
		if (typeof k !== 'string') {
			kinds[name] = k;
			continue;
		}
		const p = `/kinds/${name}`;
		const key = matchModule(k, Object.keys(modules));
		if (key === undefined) {
			errors.push({ path: p, message: `no module matches "${k}" (the app's modules: ${Object.keys(modules).join(', ') || 'none'})` });
			kinds[name] = k;
			continue;
		}
		const mod = modules[key] as { default?: unknown } | undefined;
		const obj = mod && typeof mod === 'object' && 'default' in mod ? mod.default : mod;
		if (!obj || typeof obj !== 'object' || Array.isArray(obj)) {
			errors.push({ path: p, message: `${key} is not a kind: a kind file is an object ({ model, status, drive, ... })` });
			kinds[name] = k;
			continue;
		}
		kinds[name] = obj as SceneKind;
		sources[name] = key;
	}
	return { doc: { ...doc, kinds }, errors, sources };
}

/**
 * The registry for a document whose `kinds` name component files (§3d):
 * `base` plus one entry per `component` kind, from the app's modules
 * (`import.meta.glob of the app's .svelte files, eager`) — the
 * default export is the component, `kind` its metadata. Errors, with the
 * document's paths: a path no module matches, a module with no default
 * export, and a member the component's `kind` export reads that the
 * document does not list (naut check would under-check every node of it).
 * The document's `type` wins over the export's, as it does for a built-in.
 */
export function componentRegistry(
	doc: SceneDoc,
	modules: Record<string, unknown>,
	base: NodeRegistry
): { registry: NodeRegistry; errors: SceneError[] } {
	const registry: NodeRegistry = { ...base };
	const errors: SceneError[] = [];
	for (const [name, k] of Object.entries(doc.kinds ?? {})) {
		if (typeof k === 'string' || k.component === undefined) continue;
		const p = `/kinds/${name}/component`;
		const key = matchModule(k.component, Object.keys(modules));
		if (key === undefined) {
			errors.push({ path: p, message: `no module matches "${k.component}" (the app's modules: ${Object.keys(modules).join(', ') || 'none'})` });
			continue;
		}
		const mod = modules[key] as { default?: unknown; kind?: unknown } | undefined;
		if (!mod || typeof mod.default !== 'function') {
			errors.push({ path: p, message: `${key} has no default export; a component file's default export is the component` });
			continue;
		}
		const meta = (mod.kind && typeof mod.kind === 'object' ? mod.kind : {}) as KindMeta;
		const declared = kindMembers(k);
		const missing = (meta.members ?? []).filter((m) => !declared.includes(m));
		if (missing.length)
			errors.push({ path: `/kinds/${name}/members`, message: `${key} reads ${missing.join(', ')}, which the document does not list — naut check would not hold nodes of "${name}" to it` });
		registry[name] = kindOf(mod.default as Component<NodeProps>, {
			...meta,
			type: k.type ?? meta.type,
			members: declared.length ? declared : meta.members,
			bounds: k.bounds && k.bounds !== 'auto' ? k.bounds : meta.bounds,
			labelAt: k.labelAt ?? meta.labelAt,
			status: k.status
				? ((tpl) => (value: unknown, good: boolean, over?: Record<string, unknown>) => formatStatus(tpl, value, good, over))(k.status)
				: meta.status
		});
	}
	return { registry, errors };
}

/**
 * An assembly's box: the union of its parts' boxes, each offset by the
 * part's position (rotation ignored — a halo does not need better). A
 * part whose kind reports `'auto'` bounds counts as a small box.
 */
export function assemblyBounds(a: SceneAssembly, registry: NodeRegistry): Box {
	let lo: Vec3 | undefined;
	let hi: Vec3 | undefined;
	for (const n of a.nodes) {
		const b = registry[n.kind]?.bounds;
		const box = b && b !== 'auto' ? b : FALLBACK_BOX;
		const s = n.scale ?? 1;
		const min = n.pos.map((p, i) => p + (box.center[i] - box.size[i] / 2) * s) as Vec3;
		const max = n.pos.map((p, i) => p + (box.center[i] + box.size[i] / 2) * s) as Vec3;
		lo = lo ? (lo.map((v, i) => Math.min(v, min[i])) as Vec3) : min;
		hi = hi ? (hi.map((v, i) => Math.max(v, max[i])) as Vec3) : max;
	}
	if (!lo || !hi) return FALLBACK_BOX;
	return {
		size: [hi[0] - lo[0], hi[1] - lo[1], hi[2] - lo[2]],
		center: [(hi[0] + lo[0]) / 2, (hi[1] + lo[1]) / 2, (hi[2] + lo[2]) / 2]
	};
}

/**
 * The registry a document renders with: the given registry, with the
 * document's data kinds (a `kinds` entry with a `model`) and assemblies
 * (one with an `assembly`) laid on top. A data kind under a built-in's
 * name keeps that built-in's contract, status text and faceplate and
 * replaces the geometry; a new name gets the drawer's member table and,
 * if it has a `status` template, that text. `GltfNode` is the component
 * for every model kind; it is passed in so this module never imports it
 * (it is a dynamic import, §3c) — `null` (the chunk not yet loaded, or
 * loading failed) leaves model kinds to the Svelte kind of the same name.
 * `lit` false is the flat or paper look: a model kind under a name the
 * registry knows yields to that Svelte kind (the built-in primitives are
 * the before half of a before/after), while a model kind under a new name
 * still draws its model, in that look's materials — a scene of modelled
 * kinds is never a page of labels over nothing.
 */
export function registryFor(doc: SceneDoc, registry: NodeRegistry, gltf: Component<NodeProps> | null, lit = true): NodeRegistry {
	const out: NodeRegistry = { ...registry };
	for (const [name, k] of Object.entries(doc.kinds ?? {})) {
		// A kind file not yet inlined (inlineKindFiles) defines nothing here.
		if (typeof k === 'string') continue;
		const base = registry[name];
		const tpl = k.status;
		const status = tpl ? (value: unknown, good: boolean, over?: Record<string, unknown>) => formatStatus(tpl, value, good, over) : base?.status;
		if (k.model && gltf && (lit || !base || base.fallback)) {
			out[name] = {
				component: gltf,
				data: k,
				type: k.type ?? base?.type,
				members: k.members ?? base?.members,
				bounds: k.bounds && k.bounds !== 'auto' ? k.bounds : 'auto',
				labelAt: k.labelAt,
				status,
				ports: k.ports ?? base?.ports,
				panel: base?.panel
			};
		} else if (k.assembly) {
			out[name] = {
				assembly: k.assembly,
				type: k.type,
				members: kindMembers(k),
				bounds: k.bounds && k.bounds !== 'auto' ? k.bounds : assemblyBounds(k.assembly, registry),
				labelAt: k.labelAt,
				status,
				ports: k.ports ?? base?.ports,
				panel: base?.panel
			};
		}
	}
	return out;
}
