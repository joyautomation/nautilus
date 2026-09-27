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
 * Resolve a binding map with a reader for the path: the same grammar
 * wherever a ref appears. A prop whose path is absent is left out, so the
 * component's default holds; `!ref` yields `value !== true`.
 */
export function resolveRefs(bind: Record<string, string> | undefined, read: (path: string) => unknown): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	if (!bind) return out;
	for (const [prop, ref] of Object.entries(bind)) {
		const negate = ref.startsWith('!');
		const v = read(negate ? ref.slice(1) : ref);
		if (v === undefined) continue;
		out[prop] = negate ? v !== true : v;
	}
	return out;
}

/** Resolve a node's binding map against the live tags (a document node). */
export function resolveNodeBindings(
	bind: Record<string, string> | undefined,
	tags: Record<string, unknown> | undefined
): Record<string, unknown> {
	return resolveRefs(bind, (path) => readPath(tags, path));
}

/**
 * Read a path from a node's struct, the way a PART reads it (§3d): the
 * first segment is a member of the struct — or the node's bound prop for
 * it, which wins (`bind: { pump: "P101" }` supplies `Pump`) — and the
 * rest walks down from there.
 */
export function readPart(value: unknown, over: Record<string, unknown>, path: string): unknown {
	const parts = path.split('.');
	const o = over[propFor(parts[0])];
	let cur: unknown = o !== undefined ? o : member(value, parts[0]);
	for (let i = 1; i < parts.length; i++) cur = member(cur, parts[i]);
	return cur;
}

/** The prop name a member is bound under (`Level` -> `level`); drives.ts's
 * rule, repeated here so this module stays free of the drive vocabulary. */
function propFor(memberName: string): string {
	return memberName.charAt(0).toLowerCase() + memberName.slice(1);
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
