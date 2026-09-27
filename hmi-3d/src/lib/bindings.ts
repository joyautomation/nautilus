// The binding grammar, evaluated from the document rather than hard-coded
// per component. It is the mimic's (`bind: { prop: ref }`, a leading `!`
// negates, an absent tag leaves the prop unset) with one addition struct
// tags need: a ref may be a dotted path (`P101.Speed`), walked the way the
// kit's `tagAt` walks one. The walk is repeated here rather than imported
// so this module — and its tests — stay free of the kit's Svelte entry.
// Brief: docs/design/spatial-hmi.md §3.

export { refRoot } from './scene.js';

/** Read a dotted path off a frame's tag map: `P101.Speed`. Mirrors hmi's tagAt. */
export function readPath(tags: Record<string, unknown> | undefined, path: string): unknown {
	if (!tags) return undefined;
	const parts = path.split('.');
	let cur: unknown = tags[parts[0]];
	for (let i = 1; i < parts.length; i++) {
		if (cur === null || typeof cur !== 'object') return undefined;
		cur = (cur as Record<string, unknown>)[parts[i]];
	}
	return cur;
}

/**
 * Resolve a node's binding map against the live tags. A prop whose path is
 * absent is left out, so the component's default holds; `!ref` yields
 * `value !== true`.
 */
export function resolveNodeBindings(
	bind: Record<string, string> | undefined,
	tags: Record<string, unknown> | undefined
): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	if (!bind) return out;
	for (const [prop, ref] of Object.entries(bind)) {
		const negate = ref.startsWith('!');
		const v = readPath(tags, negate ? ref.slice(1) : ref);
		if (v === undefined) continue;
		out[prop] = negate ? v !== true : v;
	}
	return out;
}

/** Are all of a binding map's root tags good? (An empty map is good.) */
export function bindingsGood(bind: Record<string, string> | undefined, isGood: (tag: string) => boolean): boolean {
	if (!bind) return true;
	for (const ref of Object.values(bind)) {
		const path = ref.startsWith('!') ? ref.slice(1) : ref;
		const dot = path.indexOf('.');
		if (!isGood(dot < 0 ? path : path.slice(0, dot))) return false;
	}
	return true;
}

/** Read a member off a struct tag value (`P101` + `Speed`). */
export function member(value: unknown, name: string): unknown {
	return value && typeof value === 'object' ? (value as Record<string, unknown>)[name] : undefined;
}

export function num(v: unknown, fallback = 0): number {
	return typeof v === 'number' && Number.isFinite(v) ? v : fallback;
}

/** Is a flow binding active? Booleans as-is; numbers above 2 (%). */
export function flowing(v: unknown): boolean {
	return typeof v === 'number' ? v > 2 : v === true;
}
