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

// ── subscribing to a subset ───────────────────────────────────────────────
// `RealtimeOptions.tags` and `GET /api/stream?tags=` take GLOB PATTERNS, and
// the controller caps how many it will accept on one connection
// (server/stream.go's `maxTagPatterns`). A screen, meanwhile, knows exactly
// which tags it draws — and on a real plant that list is routinely longer
// than the cap: the Pomona `/system` schematic binds 217 top-level tags.
//
// So something has to turn "these 217 names" into "at most 40 patterns", and
// the ONE property that must never be traded away is that the packed set is a
// SUPERSET of the names asked for. A pattern set that drops a tag does not
// error: it leaves one value on the screen reading "—" forever, which looks
// exactly like a dead instrument. Every merge below therefore widens, never
// narrows, and `packTagPatterns` is tested for that on real fleet names.

/**
 * How many `?tags=` patterns one stream connection accepts — the
 * controller's own limit (`server/stream.go`, `maxTagPatterns`). Asking for
 * more is a 400, not a truncation, so pack to this before connecting.
 */
export const MAX_TAG_PATTERNS = 40;

/**
 * A pattern that matches nothing, for a client that wants the frame's
 * NON-tag content — alarm summary, driver status, scan diagnostics, all of
 * which ride every frame whatever the filter — and none of its tags.
 *
 * There is no way to say "no tags" by omission (`tags: []` means
 * *everything*, and always has), so this is the way to say it. It is a
 * literal name rather than an unmatchable character class because it shows
 * up legibly in a network panel, and because `path.Match` rejects a
 * malformed class with a 400.
 */
export const NO_TAGS = '__no_tags__';

/**
 * Merge two patterns into the narrowest pattern that still matches
 * everything both of them match.
 *
 * Two shapes, and the choice between them is the whole art:
 *
 *  - **same length, neither wildcarded across characters** — replace the
 *    positions that differ with `?`. `RTU9_WEL15_FIT_001` +
 *    `RTU9_WEL15_FIT_002` → `RTU9_WEL15_FIT_00?`. A `?` matches exactly one
 *    character, so the widening is bounded and stays legible.
 *  - **otherwise** — the common literal prefix plus `*`. This is the blunt
 *    one: `*` spans the whole rest of the name (a nautilus tag contains no
 *    `/`, `path.Match`'s only separator), so `RTU32_*` pulls all 514 tags on
 *    that panel. It is used only where a `?` merge cannot apply.
 */
export function mergeTagPatterns(a: string, b: string): string {
	if (a === b) return a;
	if (a.length === b.length && !a.includes('*') && !b.includes('*')) {
		let out = '';
		for (let i = 0; i < a.length; i++) out += a[i] === b[i] ? a[i] : '?';
		return out;
	}
	// The common LITERAL prefix: a `?` or `*` in either operand ends it,
	// because the merged prefix has to be a prefix of every name both of
	// them match, and a wildcard is not.
	let k = 0;
	const n = Math.min(a.length, b.length);
	while (k < n && a[k] === b[k] && a[k] !== '?' && a[k] !== '*') k++;
	return a.slice(0, k) + '*';
}

// What one merge costs, in "how much extra plant does this drag in".
//
// A `?` merge is charged per position it newly wildcards. A `*` merge is
// charged a flat 1000 plus the length of the tail it gives up, which puts it
// behind every possible `?` merge: the greedy loop wildcards characters
// across the whole set before it truncates anything. That ordering is the
// one thing here that matters, because the two are not comparable in scale —
// a `?` admits at most one character's worth of siblings per position, while
// a `*` on a short prefix admits an entire panel (`RTU32_*` is 514 tags on
// the Pomona host).
//
// MEASURED, on the fleet this was built for: `/system`'s 217 top-level tags
// pack into 40 patterns matching **200** of the controller's 10,236 — 187 of
// them the tags actually asked for, 13 of them neighbours the wildcards
// swept up. Which is to say the widening costs about 7 %, and a cleverer
// cost function has almost nothing left to win. (A prefix-only packer, for
// comparison — merging on common prefixes and `*` alone, no `?` — matches
// 7,593 of them. The `?` is where the whole result comes from.)
function mergeCost(a: string, b: string): number {
	const m = mergeTagPatterns(a, b);
	if (m.endsWith('*')) return 1000 + (Math.max(a.length, b.length) - (m.length - 1)) * 10;
	let added = 0;
	let total = 0;
	for (let i = 0; i < m.length; i++) {
		if (m[i] !== '?') continue;
		total++;
		if (a[i] !== '?') added++;
	}
	// The tiebreak keeps an already-wide pattern from being widened again
	// for free when a narrow merge costs the same.
	return added + total / 1000;
}

/**
 * Pack a screen's tag list into at most `max` glob patterns, widening as
 * little as possible.
 *
 * Give it TOP-LEVEL tag names (the frame's own keys — `RTU9_WEL15_FIT_001`,
 * not `RTU9_WEL15_FIT_001.VALUE`): the controller matches `?tags=` against
 * those, and a UDT's members ride along with their root. Fewer names than
 * `max` are returned verbatim, so the common case subscribes to exact names
 * and pulls exactly them.
 *
 * ```ts
 * packTagPatterns(['RTU9_WEL15_FIT_001', 'RTU9_WEL15_FIT_002', …], { max: 2 })
 * // → ['RTU9_WEL15_FIT_00?', …]
 * ```
 *
 * **The result always matches every name given.** Merges only ever widen
 * (see `mergeTagPatterns`), which is what makes a too-small `max` cost bytes
 * rather than correctness — the failure mode of the other direction is a
 * screen that renders a live plant as a wall of dashes.
 *
 * An empty list packs to `[NO_TAGS]`, not to `[]`: `[]` means *everything*
 * to `RealtimeOptions.tags`, which is the opposite of what a caller with
 * nothing to draw is asking for.
 */
export function packTagPatterns(names: string[], opts: { max?: number } = {}): string[] {
	const max = Math.max(1, Math.floor(opts.max ?? MAX_TAG_PATTERNS));
	const pats = [...new Set(names.filter((n) => n.length > 0))].sort();
	if (pats.length === 0) return [NO_TAGS];
	if (pats.length <= max) return pats;

	// Agglomerative merge, cheapest pair first. The candidate heap is built
	// once over every pair and then extended only with the pairs the newest
	// group creates, with dead entries dropped lazily on pop — otherwise
	// this is O(n³) and a 200-tag screen spends a visible fraction of a
	// second on it during navigation.
	const alive: boolean[] = pats.map(() => true);
	const heap: { c: number; i: number; j: number }[] = [];
	const push = (c: number, i: number, j: number) => {
		heap.push({ c, i, j });
		let k = heap.length - 1;
		while (k > 0) {
			const p = (k - 1) >> 1;
			if (heap[p].c <= heap[k].c) break;
			[heap[p], heap[k]] = [heap[k], heap[p]];
			k = p;
		}
	};
	const pop = () => {
		const top = heap[0];
		const last = heap.pop()!;
		if (heap.length) {
			heap[0] = last;
			let k = 0;
			for (;;) {
				const l = 2 * k + 1;
				const r = l + 1;
				let s = k;
				if (l < heap.length && heap[l].c < heap[s].c) s = l;
				if (r < heap.length && heap[r].c < heap[s].c) s = r;
				if (s === k) break;
				[heap[s], heap[k]] = [heap[k], heap[s]];
				k = s;
			}
		}
		return top;
	};

	for (let i = 0; i < pats.length; i++) {
		for (let j = i + 1; j < pats.length; j++) push(mergeCost(pats[i], pats[j]), i, j);
	}

	let live = pats.length;
	while (live > max && heap.length) {
		const { i, j } = pop();
		if (!alive[i] || !alive[j]) continue;
		const merged = mergeTagPatterns(pats[i], pats[j]);
		alive[i] = false;
		alive[j] = false;
		pats.push(merged);
		alive.push(true);
		const k = pats.length - 1;
		for (let t = 0; t < k; t++) if (alive[t]) push(mergeCost(pats[t], merged), t, k);
		live--;
	}

	return pats.filter((_, i) => alive[i]).sort();
}

// `path.Match` (Go) as a regular expression, for the client side of a
// subscription: `*` and `?` stop at the separator `/`, which no nautilus tag
// name contains, so both span a dotted name. Character classes are passed
// through with `!` translated to `^`.
function patternRegExp(pattern: string): RegExp {
	let src = '';
	for (let i = 0; i < pattern.length; i++) {
		const c = pattern[i];
		if (c === '*') src += '[^/]*';
		else if (c === '?') src += '[^/]';
		else if (c === '[') {
			let j = i + 1;
			let cls = '';
			if (pattern[j] === '!' || pattern[j] === '^') {
				cls += '^';
				j++;
			}
			for (; j < pattern.length && pattern[j] !== ']'; j++) {
				cls += pattern[j] === '\\' ? '\\\\' : pattern[j].replace(/[\]^\\]/g, '\\$&');
			}
			if (j >= pattern.length) return /(?!)/; // unterminated class matches nothing
			src += `[${cls}]`;
			i = j;
		} else src += c.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
	}
	return new RegExp(`^${src}$`);
}

const rxCache = new Map<string, RegExp>();

/** Does one glob pattern match this tag name, the way the controller
 *  matches it (`path.Match` semantics)? */
export function tagPatternMatches(pattern: string, name: string): boolean {
	let rx = rxCache.get(pattern);
	if (!rx) {
		rx = patternRegExp(pattern);
		rxCache.set(pattern, rx);
	}
	return rx.test(name);
}

/**
 * Is this tag inside a subscription? An EMPTY pattern list means the
 * unfiltered stream — everything — exactly as `RealtimeOptions.tags` reads
 * it, so the answer there is always true.
 *
 * Worth having on the client because "the runtime does not publish this
 * point" and "this screen did not ask for it" look identical in a frame, and
 * only the first one deserves the not-published treatment on the glass.
 */
export function tagInPatterns(patterns: string[], name: string): boolean {
	if (patterns.length === 0) return true;
	return patterns.some((p) => tagPatternMatches(p, name));
}
