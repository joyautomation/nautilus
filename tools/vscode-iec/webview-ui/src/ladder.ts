// The ladder render model (mirror of lang/ld.Model) and the pure
// power-flow evaluator: given live values, compute whether each element
// passes power — contacts from their tag, in-rung FBs from their REAL
// output pin (inst.Q streams, so the block's truth is exact), comparisons
// evaluated locally. Anything unknowable stays undefined and renders
// neutral rather than guessing.

export type LdElement = {
	kind: 'contact' | 'branch' | 'fn' | 'fb' | 'coil';
	ref?: string;
	neg?: boolean;
	mode?: string; // coil: "" | "S" | "R"
	fn?: string;
	args?: string;
	inst?: string;
	type?: string;
	powerIn?: string;
	powerOut?: string;
	legs?: LdElement[][];
	// diff overlay only (never present in models from Go): how this element
	// relates to the base version, and what it looked like there.
	_diff?: 'added' | 'removed' | 'changed';
	_was?: string;
};
export type LdRung = {
	name: string;
	comment?: string;
	line: number;
	endLine?: number;
	elements: LdElement[];
	coils: LdElement[];
	/** The FUNCTION_BLOCK this rung belongs to; absent for the PROGRAM's
	 * own rungs. A .ld file may define ladder subroutines alongside (or
	 * instead of) its program — see docs/functions.md. */
	pou?: string;
	/** The rung's elements sit on the RUNG header line itself. */
	inline?: boolean;
};
export type LdPin = { name: string; type: string; dir: 'in' | 'out' };
/** A FUNCTION_BLOCK POU defined in the file: a group of rungs. */
export type LdBlock = { name: string; line: number; endLine: number; pins?: LdPin[] };
export type LdVar = { name: string; type: string; init?: string; section: string; line: number; pou?: string };
export type LdComment = { line: number; endLine: number; text: string };
export type LdModel = {
	name: string;
	vars?: LdVar[];
	rungs: LdRung[];
	comments?: LdComment[];
	blocks?: LdBlock[];
};

/** One element annotated with its power state. */
export type Ann = {
	el: LdElement;
	in?: boolean; // power arriving from the left (undefined = unknown)
	out?: boolean; // power leaving to the right
	val?: boolean; // the element's own state (contact closed, coil energized)
	legs?: Ann[][];
};

type Resolve = (label: string) => unknown;

function truthy(v: unknown): boolean | undefined {
	if (v === undefined || v === null) return undefined;
	if (typeof v === 'boolean') return v;
	if (typeof v === 'number') return v !== 0;
	return undefined;
}

function num(v: unknown): number | undefined {
	if (typeof v === 'number') return v;
	if (typeof v === 'boolean') return v ? 1 : 0;
	return undefined;
}

/** Split a verbatim argument string on top-level commas. */
export function splitArgs(args: string): string[] {
	const out: string[] = [];
	let depth = 0;
	let cur = '';
	for (const c of args) {
		if (c === '(' || c === '[') depth++;
		if (c === ')' || c === ']') depth--;
		if (c === ',' && depth === 0) {
			out.push(cur.trim());
			cur = '';
			continue;
		}
		cur += c;
	}
	if (cur.trim() !== '') out.push(cur.trim());
	return out;
}

/** Resolve one operand: a numeric literal, TRUE/FALSE, or a live label. */
function operand(text: string, resolve: Resolve): unknown {
	const t = text.trim();
	if (/^-?\d+(\.\d+)?$/.test(t)) return parseFloat(t);
	if (/^TRUE$/i.test(t)) return true;
	if (/^FALSE$/i.test(t)) return false;
	return resolve(t);
}

/** Evaluate a function contact when we know how; undefined otherwise. */
export function evalFn(fn: string, args: string, resolve: Resolve): boolean | undefined {
	const parts = splitArgs(args).map((a) => operand(a, resolve));
	const [a, b] = [num(parts[0]), num(parts[1])];
	switch (fn.toUpperCase()) {
		case 'GT':
			return a !== undefined && b !== undefined ? a > b : undefined;
		case 'GE':
			return a !== undefined && b !== undefined ? a >= b : undefined;
		case 'LT':
			return a !== undefined && b !== undefined ? a < b : undefined;
		case 'LE':
			return a !== undefined && b !== undefined ? a <= b : undefined;
		case 'EQ':
			return a !== undefined && b !== undefined ? a === b : undefined;
		case 'NE':
			return a !== undefined && b !== undefined ? a !== b : undefined;
		case 'NOT': {
			const v = truthy(parts[0]);
			return v === undefined ? undefined : !v;
		}
		case 'AND': {
			let all: boolean | undefined = true;
			for (const p of parts) {
				const v = truthy(p);
				if (v === false) return false;
				if (v === undefined) all = undefined;
			}
			return all;
		}
		case 'OR': {
			let any: boolean | undefined = false;
			for (const p of parts) {
				const v = truthy(p);
				if (v === true) return true;
				if (v === undefined) any = undefined;
			}
			return any;
		}
	}
	return undefined;
}

/** and3: three-valued AND (undefined = unknown). */
function and3(a: boolean | undefined, b: boolean | undefined): boolean | undefined {
	if (a === false || b === false) return false;
	if (a === undefined || b === undefined) return undefined;
	return true;
}

/** Annotate a series with power flow, given the power arriving at its left. */
export function annotate(elems: LdElement[], inPower: boolean | undefined, resolve: Resolve): Ann[] {
	const out: Ann[] = [];
	let power = inPower;
	for (const el of elems) {
		const ann: Ann = { el, in: power };
		switch (el.kind) {
			case 'contact': {
				const v = truthy(resolve(el.ref ?? ''));
				ann.val = v === undefined ? undefined : el.neg ? !v : v;
				ann.out = and3(power, ann.val);
				break;
			}
			case 'fn': {
				ann.val = evalFn(el.fn ?? '', el.args ?? '', resolve);
				ann.out = and3(power, ann.val);
				break;
			}
			case 'fb': {
				// The instance's boolean output streams — exact truth, no
				// modeling needed.
				ann.val = truthy(resolve((el.inst ?? '') + '.' + (el.powerOut ?? 'Q')));
				ann.out = ann.val;
				break;
			}
			case 'branch': {
				ann.legs = (el.legs ?? []).map((leg) => annotate(leg, power, resolve));
				let any: boolean | undefined = false;
				for (const leg of ann.legs) {
					const legOut = leg.length ? leg[leg.length - 1].out : power;
					if (legOut === true) any = true;
					else if (legOut === undefined && any !== true) any = undefined;
				}
				ann.val = any;
				ann.out = any;
				break;
			}
			case 'coil': {
				ann.val = truthy(resolve(el.ref ?? ''));
				ann.out = power;
				break;
			}
		}
		out.push(ann);
		power = ann.out;
	}
	return out;
}

// ── diff (rung-granular) ────────────────────────────────────────────────────

export type RungStatus = 'added' | 'removed' | 'changed';

/** One line of what an element looked like, for "was …" tooltips. */
function elText(e: LdElement): string {
	switch (e.kind) {
		case 'contact':
			return (e.neg ? '/' : '') + (e.ref ?? '');
		case 'coil':
			return `( ${e.mode ? e.mode + ' ' : ''}${e.ref ?? ''} )`;
		case 'fn':
			return `${e.fn}(${e.args ?? ''})`;
		case 'fb':
			return `${e.inst}:${e.type}(${e.args ?? ''})`;
		default:
			return '[ branch ]';
	}
}

/** Element-level diff of one series: LCS alignment on structural identity,
 * then contiguous remove/add runs pair up same-kind elements as "changed"
 * (a retag or new args marks ONE element, not a ghost plus a fresh one).
 * Removed elements stay in the merged series, ghosted where they lived.
 * Same-kind branches diff leg by leg, recursively. */
function diffSeries(base: LdElement[], head: LdElement[]): LdElement[] {
	const bk = base.map((e) => JSON.stringify(e));
	const hk = head.map((e) => JSON.stringify(e));
	const n = base.length;
	const m = head.length;
	const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
	for (let i = n - 1; i >= 0; i--) {
		for (let j = m - 1; j >= 0; j--) {
			dp[i][j] = bk[i] === hk[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
		}
	}
	const out: LdElement[] = [];
	const removed: LdElement[] = [];
	const added: LdElement[] = [];
	const flush = () => {
		while (removed.length && added.length && removed[0].kind === added[0].kind) {
			const b = removed.shift()!;
			const h = added.shift()!;
			if (b.kind === 'branch' && h.kind === 'branch') {
				const bl = b.legs ?? [];
				const hl = h.legs ?? [];
				const legs: LdElement[][] = [];
				for (let x = 0; x < Math.max(bl.length, hl.length); x++) {
					if (x < bl.length && x < hl.length) legs.push(diffSeries(bl[x], hl[x]));
					else if (x < hl.length) legs.push(hl[x].map((e) => ({ ...e, _diff: 'added' as const })));
					else legs.push(bl[x].map((e) => ({ ...e, _diff: 'removed' as const })));
				}
				out.push({ ...h, legs });
			} else {
				out.push({ ...h, _diff: 'changed', _was: elText(b) });
			}
		}
		out.push(...removed.map((e) => ({ ...e, _diff: 'removed' as const })));
		out.push(...added.map((e) => ({ ...e, _diff: 'added' as const })));
		removed.length = 0;
		added.length = 0;
	};
	let i = 0;
	let j = 0;
	while (i < n || j < m) {
		if (i < n && j < m && bk[i] === hk[j]) {
			flush();
			out.push(head[j]);
			i++;
			j++;
		} else if (j < m && (i >= n || dp[i][j + 1] >= dp[i + 1][j])) {
			added.push(head[j]);
			j++;
		} else {
			removed.push(base[i]);
			i++;
		}
	}
	flush();
	return out;
}

/** Overlay base onto head: rungs are keyed by name (unique per program);
 * a rung present in both but different gets an ELEMENT-level overlay —
 * added/removed/changed marks on the individual contacts, coils, and
 * blocks. Removed rungs are spliced back in near where they lived. */
export function diffLd(
	base: LdModel,
	head: LdModel
): { model: LdModel; status: Record<string, RungStatus> } {
	const status: Record<string, RungStatus> = {};
	const shape = (r: LdRung) => JSON.stringify({ c: r.comment ?? '', e: r.elements, k: r.coils });
	const baseBy = new Map(base.rungs.map((r) => [r.name.toLowerCase(), r]));
	const out: LdRung[] = [];
	for (const r of head.rungs) {
		const b = baseBy.get(r.name.toLowerCase());
		if (!b) {
			status[r.name] = 'added';
			out.push(r);
		} else if (shape(b) !== shape(r)) {
			status[r.name] = 'changed';
			out.push({ ...r, elements: diffSeries(b.elements, r.elements), coils: diffSeries(b.coils, r.coils) });
		} else {
			out.push(r);
		}
	}
	let insertAt = 0;
	for (const b of base.rungs) {
		const idx = out.findIndex((r) => r.name.toLowerCase() === b.name.toLowerCase());
		if (idx >= 0) {
			insertAt = idx + 1;
			continue;
		}
		out.splice(insertAt, 0, b);
		status[b.name] = 'removed';
		insertAt++;
	}
	return { model: { ...head, rungs: out }, status };
}
