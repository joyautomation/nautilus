// The ladder render model (mirror of lang/ld.Model) and the pure
// power-flow evaluator: given live values, compute whether each element
// passes power — contacts from their tag, in-rung FBs from their REAL
// output pin (inst.Q streams, so the block's truth is exact), comparisons
// evaluated locally. Anything unknowable stays undefined and renders
// neutral rather than guessing.

export type LdElement = {
	kind: 'contact' | 'edge' | 'branch' | 'fn' | 'fb' | 'coil' | 'assign';
	ref?: string;
	/** assign: the element's body, `y := a + b; z := 0`. */
	text?: string;
	neg?: boolean;
	mode?: string; // coil: "" | "S" | "R" | "P" | "N"; edge: "P" | "N"
	/** edge: the implicit R_TRIG / F_TRIG instance (`rt_<rung>_<ref>`),
	 * whose .Q is the contact's one-scan state. */
	trig?: string;
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
	/** Where a Logix rung's operands resolve — "Program:MainProgram" or
	 * "AOI:Valve". Absent for nautilus source. */
	scope?: string;
};
export type LdPin = { name: string; type: string; dir: 'in' | 'out' };
/** A FUNCTION_BLOCK POU defined in the file: a group of rungs. */
export type LdBlock = { name: string; line: number; endLine: number; pins?: LdPin[] };
export type LdVar = { name: string; type: string; init?: string; section: string; line: number; pou?: string };
export type LdComment = { line: number; endLine: number; text: string };
/** One insertable block type (lang/ld catalog.go): the standard blocks,
 * then the user FUNCTION_BLOCKs in scope (this file + project libraries). */
export type LdFbType = {
	name: string;
	detail?: string;
	user?: boolean;
	powerIn?: string;
	powerOut?: string;
	pins?: { name: string; type: string; dir: 'in' | 'out' | 'inout' }[];
	/** Starting argument text for a fresh insert. */
	args?: string;
	/** A fresh instance is the first free <prefix><n>. */
	prefix: string;
};
/** A tag nautilus.yaml declares (sent with the model inside a project). */
export type LdTag = { name: string; type?: string; role?: string; unit?: string; desc?: string };
export type LdModel = {
	name: string;
	vars?: LdVar[];
	rungs: LdRung[];
	comments?: LdComment[];
	blocks?: LdBlock[];
	/** The FB picker's catalog (absent from an older CLI). */
	fbTypes?: LdFbType[];
	/** The project manifest's tags, for declare-on-retag. */
	tags?: LdTag[];
	/** Names the project's libraries put in scope that need no declaration:
	 * VAR_GLOBAL CONSTANT constants and enumeration members. */
	known?: string[];
	/** A whitespace-only source: no POU yet; the first op seeds one. */
	blank?: boolean;
};

/** Arrays always arrays: an older CLI (or saved webview state) can carry
 * `null` for an empty body's rungs, or a rung's elements/coils. */
export function normalizeLd(m: LdModel): LdModel {
	return {
		...m,
		rungs: (m.rungs ?? []).map((r) => ({ ...r, elements: r.elements ?? [], coils: r.coils ?? [] }))
	};
}

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

const ST_WORDS = new Set(['and', 'or', 'xor', 'not', 'mod', 'true', 'false']);

/** The variables a function contact's or block's argument text reads or
 * writes, as written (base names: `t1.Q` → `t1`, `Levels[i]` → `Levels`,
 * `i`): the right side of every `pin := expr` and `pin => target`, and each
 * positional argument. Pin names, function names (`LIMIT(`), typed literals
 * (`T#5S`, `16#FF`), numbers, strings and ST operators are not variables. */
export function argIdents(args: string): string[] {
	const out: string[] = [];
	const seen = new Set<string>();
	for (const part of splitArgs(args ?? '')) {
		const m = /^\s*[A-Za-z_][A-Za-z0-9_]*\s*(:=|=>)([\s\S]*)$/.exec(part);
		const expr = m ? m[2] : part;
		const tok = /'[^']*'|"[^"]*"|[A-Za-z_][A-Za-z0-9_]*#[A-Za-z0-9_.:+-]*|\d[\w.#]*|\.\s*[A-Za-z_][A-Za-z0-9_]*|[A-Za-z_][A-Za-z0-9_]*(?=\s*(\()?)/g;
		for (let t: RegExpExecArray | null; (t = tok.exec(expr)); ) {
			const w = t[0];
			if (!/^[A-Za-z_]/.test(w) || w.includes('#') || t[1] === '(') continue;
			if (ST_WORDS.has(w.toLowerCase()) || w === '_') continue;
			if (!seen.has(w.toLowerCase())) {
				seen.add(w.toLowerCase());
				out.push(w);
			}
		}
	}
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
			case 'edge': {
				// The one-shot's own output streams with the program's
				// instances (exact); without it, the input still rules out
				// a pulse: a rising edge can't fire while its tag is FALSE,
				// a falling one while it is TRUE. Otherwise: unknown.
				const q = el.trig ? truthy(resolve(el.trig + '.Q')) : undefined;
				if (q !== undefined) ann.val = q;
				else {
					const v = truthy(resolve(el.ref ?? ''));
					ann.val = v === undefined ? undefined : (el.mode === 'N') === v ? false : undefined;
				}
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
			case 'assign': {
				// Made when power reaches it; power passes through.
				ann.val = power;
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
		case 'edge':
			return (e.mode === 'N' ? '-' : '+') + (e.ref ?? '');
		case 'coil':
			return `( ${e.mode ? e.mode + ' ' : ''}${e.ref ?? ''} )`;
		case 'fn':
			return `${e.fn}(${e.args ?? ''})`;
		case 'fb':
			return `${e.inst}:${e.type}(${e.args ?? ''})`;
		case 'assign':
			return `{ ${e.text ?? ''} }`;
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

// ── what a use says a name is (the declare offer's type) ────────────────────

const PLAIN = /^[A-Za-z_][A-Za-z0-9_]*$/;
const NUMERIC = /^(S|D|L|U|US|UD|UL)?INT$|^L?REAL$|^(BYTE|WORD|DWORD|LWORD)$/;
const COMPARE = new Set(['GT', 'GE', 'LT', 'LE', 'EQ', 'NE', 'MAX', 'MIN', 'ADD', 'SUB', 'MUL', 'DIV', 'MOD']);

/** The type each name the given rungs use is used AS, keyed by lower-case
 * name — the type its declaration should take. Strongest first: a block
 * pin it binds (`CV => Count` on a CTU is INT; a user block's pin is what
 * the block declares), then the other operand of a comparison (a typed
 * literal `INT#5`, a REAL literal `90.0`, or a declared variable), then
 * BOOL for a plain contact or coil. A use that says nothing (an untyped
 * integer literal could be any number) leaves the name out. */
export function useTypes(rungs: LdRung[], fbTypes: LdFbType[] = [], vars: LdVar[] = []): Map<string, string> {
	const out = new Map<string, { type: string; rank: number }>();
	const put = (name: string, type: string, rank: number) => {
		const k = name.toLowerCase();
		const had = out.get(k);
		if (!had || rank > had.rank) out.set(k, { type: type.toUpperCase(), rank });
	};
	const declared = new Map(vars.map((v) => [v.name.toLowerCase(), v.type.toUpperCase()]));
	const pinTypes = new Map(
		fbTypes.map((t) => [t.name.toLowerCase(), new Map((t.pins ?? []).map((p) => [p.name.toLowerCase(), p.type]))])
	);
	const literalType = (s: string): string | undefined => {
		const t = s.trim();
		const typed = /^([A-Za-z_]+)#/.exec(t);
		if (typed && NUMERIC.test(typed[1].toUpperCase())) return typed[1].toUpperCase();
		if (/^-?\d+\.\d*([eE][-+]?\d+)?$/.test(t)) return 'REAL';
		if (PLAIN.test(t)) return declared.get(t.toLowerCase());
		return undefined;
	};
	const walk = (els: LdElement[]) => {
		for (const e of els ?? []) {
			if ((e.kind === 'contact' || e.kind === 'edge' || e.kind === 'coil') && e.ref && PLAIN.test(e.ref) && e.ref !== '_') {
				put(e.ref, 'BOOL', 1);
			}
			if (e.kind === 'fb' && e.args) {
				const pins = pinTypes.get((e.type ?? '').toLowerCase());
				for (const part of splitArgs(e.args)) {
					const m = /^\s*([A-Za-z_][A-Za-z0-9_]*)\s*(:=|=>)\s*(.*?)\s*$/.exec(part);
					const type = m && pins?.get(m[1].toLowerCase());
					if (m && type && PLAIN.test(m[3]) && m[3] !== '_') put(m[3], type, 3);
				}
			}
			if (e.kind === 'fn' && COMPARE.has((e.fn ?? '').toUpperCase())) {
				const args = splitArgs(e.args ?? '');
				for (const [i, a] of args.entries()) {
					if (!PLAIN.test(a) || a === '_' || declared.has(a.toLowerCase())) continue;
					const other = args.filter((_, j) => j !== i).map(literalType).find((t) => t && NUMERIC.test(t));
					if (other) put(a, other, 2);
				}
			}
			for (const leg of e.legs ?? []) walk(leg);
		}
	};
	for (const r of rungs) {
		walk(r.elements);
		walk(r.coils);
	}
	return new Map([...out].map(([k, v]) => [k, v.type]));
}

/** The type the declare offer gives a name: its use's, over a manifest
 * REAL — the manifest types a tag from its seed, and a number seed (`init:
 * 0`) only says "a number" — else the manifest's, else BOOL. */
export function offerType(use: string | undefined, tagType: string | undefined): string {
	if (!tagType) return use ?? 'BOOL';
	if (tagType.toUpperCase() === 'REAL' && use && NUMERIC.test(use)) return use;
	return tagType;
}
