// Reading a frame's tag map: dotted struct paths, and what is in there.
//
// A nautilus frame carries `tags` as a nested object — a UDT tag lands as a
// struct, and its members are addressed the way Sparkplug and the write path
// address them, `AI_001.LVL.CTL1HSP`. These helpers resolve that, and they all
// return `undefined` rather than a default when any hop is missing: a screen
// bound to a tag the runtime does not publish must show "—", never a confident
// zero.

/** A frame's tag map, or any nested plain-object tree of values. */
export type TagTree = Record<string, unknown>;

/**
 * Resolve a dotted struct path against a tag tree.
 * `tagAt(tags, 'WEL15_SUP_015.LVL.OUT')` walks the nested structs.
 * Returns `undefined` when any hop is missing.
 */
export function tagAt(tags: TagTree | undefined, path: string): unknown {
	if (!tags) return undefined;
	const parts = path.split('.');
	let cur: unknown = tags[parts[0]];
	for (let i = 1; i < parts.length; i++) {
		if (cur === null || typeof cur !== 'object') return undefined;
		cur = (cur as TagTree)[parts[i]];
	}
	return cur;
}

/** The number at `path`, or `dflt` (default `NaN`) when it is absent or not
 *  a finite number. Pass `0` for `dflt` only where a zero is genuinely safe. */
export function numAt(tags: TagTree | undefined, path: string, dflt = NaN): number {
	const v = tagAt(tags, path);
	return typeof v === 'number' && Number.isFinite(v) ? v : dflt;
}

/** True only when the value at `path` is exactly `true`. */
export function boolAt(tags: TagTree | undefined, path: string): boolean {
	return tagAt(tags, path) === true;
}

/** True when the runtime publishes this path at all — as distinct from it
 *  reading zero or false. */
export function hasTagAt(tags: TagTree | undefined, path: string): boolean {
	return tagAt(tags, path) !== undefined;
}

/** One numeric leaf found by `numericLeaves`. */
export interface NumericLeaf {
	/** Full dotted path from the top-level tag down to the number. */
	path: string;
	/** Top-level tag the leaf lives under — the natural grouping key. */
	root: string;
	value: number;
}

/**
 * Every finite number in a tag tree, as dotted paths.
 *
 * This is what a tag PICKER should be built from, rather than a static tag
 * list: it enumerates exactly what this runtime is publishing right now, so
 * the picker can never offer a tag that resolves to nothing. Arrays are
 * treated as leaves and skipped — a trend pen wants a scalar.
 *
 * ```ts
 * const leaves = $derived(numericLeaves(rt.tags));
 * // → [{ path: 'WEL15_FIT_001.VALUE', root: 'WEL15_FIT_001', value: 1886.05 }, …]
 * ```
 */
export function numericLeaves(tags: TagTree | undefined, opts: { maxDepth?: number } = {}): NumericLeaf[] {
	const maxDepth = opts.maxDepth ?? 12;
	const out: NumericLeaf[] = [];
	if (!tags) return out;

	const walk = (path: string, root: string, v: unknown, depth: number) => {
		if (typeof v === 'number' && Number.isFinite(v)) {
			out.push({ path, root, value: v });
			return;
		}
		if (depth >= maxDepth) return;
		if (v === null || typeof v !== 'object' || Array.isArray(v)) return;
		for (const [k, vv] of Object.entries(v as TagTree)) {
			walk(`${path}.${k}`, root, vv, depth + 1);
		}
	};

	for (const [top, v] of Object.entries(tags)) walk(top, top, v, 0);
	return out.sort((a, b) => a.path.localeCompare(b.path));
}
