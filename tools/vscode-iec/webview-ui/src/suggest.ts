// Completion vocabulary + filtering for the diagram's text fields. Free
// text with suggestions, in the spirit of intellisense: nothing is ever
// forced — an unknown name still lands and becomes a diagnostic to follow.

export type SuggestItem = { name: string; detail?: string };

// Operators/functions the FBD transpiler and IR builtins understand.
export const FUNCTIONS: SuggestItem[] = [
	'AND', 'OR', 'XOR', 'NOT', 'ADD', 'SUB', 'MUL', 'DIV', 'MOD', 'MOVE',
	'GT', 'GE', 'LT', 'LE', 'EQ', 'NE',
	'SEL', 'MUX', 'MIN', 'MAX', 'LIMIT',
	'ABS', 'SQRT', 'EXPT', 'TRUNC', 'SIN', 'COS', 'TAN', 'ASIN', 'ACOS', 'ATAN', 'ATAN2',
	'LN', 'LOG', 'EXP', 'SHL', 'SHR', 'ROL', 'ROR',
	'LEN', 'CONCAT', 'LEFT', 'RIGHT', 'MID', 'FIND', 'INSERT', 'DELETE', 'REPLACE'
].map((name) => ({ name }));

// Standard function block types (lang/ir/builtins_fb.go) — the fallback
// only: the real list is the catalog each model carries (fbTypes, from
// lang/fbcatalog: these plus every user FUNCTION_BLOCK in scope, with pins).
export const FB_TYPES: SuggestItem[] = [
	{ name: 'TON', detail: 'on-delay timer' },
	{ name: 'TOF', detail: 'off-delay timer' },
	{ name: 'TP', detail: 'pulse timer' },
	{ name: 'CTU', detail: 'count up' },
	{ name: 'CTD', detail: 'count down' },
	{ name: 'CTUD', detail: 'count up/down' },
	{ name: 'R_TRIG', detail: 'rising edge' },
	{ name: 'F_TRIG', detail: 'falling edge' },
	{ name: 'SR', detail: 'set-dominant latch' },
	{ name: 'RS', detail: 'reset-dominant latch' },
	{ name: 'PID', detail: 'closed-loop control' }
];

/** One insertable block type (mirror of lang/fbcatalog.Type). */
export type FbCatalogType = {
	name: string;
	detail?: string;
	user?: boolean;
	pins?: { name: string; type: string; dir: 'in' | 'out' | 'inout' }[];
	args?: string;
	prefix: string;
};

/** The model's catalog; an older CLI sends none, and the standard names
 * still list (without pins — their args are the author's to type). */
export function fbCatalog<T extends FbCatalogType>(types: T[] | undefined): FbCatalogType[] {
	if (types?.length) return types;
	return FB_TYPES.map((t) => ({
		name: t.name,
		detail: t.detail,
		prefix: t.name === 'PID' ? 'pid' : (/^[A-Z]+/.exec(t.name)?.[0] ?? 'fb').slice(0, 2).toLowerCase()
	}));
}

/** A fresh insert's arguments: every input and in-out open (`_`). */
export function openArgs(t: FbCatalogType): string {
	if (t.args) return t.args;
	return (t.pins ?? [])
		.filter((p) => p.dir !== 'out')
		.map((p) => `${p.name} := _`)
		.join(', ');
}

/** An FB instance on the diagram, with its output pins. */
export type FbInst = { name: string; type?: string; outs: string[] };

/** Every instance output as a readable source: lic.CV, lic.SAT_HI, … */
export function fbOutputRefs(insts: FbInst[]): SuggestItem[] {
	const out: SuggestItem[] = [];
	for (const i of insts) {
		for (const p of i.outs) out.push({ name: `${i.name}.${p}`, detail: i.type ? `${i.type} output` : 'output' });
	}
	return out;
}

// Declarable elementary types (lang/st).
export const TYPES: SuggestItem[] = [
	'BOOL', 'INT', 'DINT', 'UINT', 'UDINT', 'WORD', 'REAL', 'LREAL', 'TIME', 'STRING'
].map((name) => ({ name }));

const LIMIT_SHOWN = 10;

/** Case-insensitive match, prefix hits ranked before substring hits. */
export function filterItems(items: SuggestItem[], token: string): SuggestItem[] {
	const t = token.toLowerCase();
	if (!t) return items.slice(0, LIMIT_SHOWN);
	const pre: SuggestItem[] = [];
	const sub: SuggestItem[] = [];
	for (const it of items) {
		const n = it.name.toLowerCase();
		if (n === t) continue; // already typed exactly — nothing to offer
		if (n.startsWith(t)) pre.push(it);
		else if (n.includes(t)) sub.push(it);
	}
	return pre.concat(sub).slice(0, LIMIT_SHOWN);
}

/** The function name of a call being edited ("GT(_, 0.0)" → "GT"). */
export function leadingIdent(value: string): string {
	return /^\s*([A-Za-z_][A-Za-z0-9_]*)/.exec(value)?.[1] ?? '';
}

/** Replace just the function name, keeping the argument list intact. */
export function replaceLeadingIdent(value: string, name: string): string {
	const m = /^(\s*)([A-Za-z_][A-Za-z0-9_]*)?([\s\S]*)$/.exec(value)!;
	const rest = m[3] ?? '';
	return m[1] + name + (rest.trim() ? rest : '(_)');
}

/** The token being completed in a comma-separated list. */
export function lastToken(value: string): string {
	const i = value.lastIndexOf(',');
	return i === -1 ? value : value.slice(i + 1);
}

/** Replace that token, preserving everything before the last comma. */
export function replaceLastToken(value: string, name: string): string {
	const i = value.lastIndexOf(',');
	return i === -1 ? name : value.slice(0, i + 1) + ' ' + name;
}

/** The word being completed at the end of "Q target" (an SFC action
 * association): whatever follows the last space. */
export function tailWord(value: string): string {
	const m = /(\S*)$/.exec(value);
	return m ? m[1] : '';
}

/** Replace that last word, keeping the qualifier (and its space) before
 * it; a lone word with no qualifier yet gets the default "N ". */
export function replaceTailWord(value: string, name: string): string {
	const i = value.search(/\S*$/);
	const head = value.slice(0, i);
	return (head.trim() ? head : 'N ') + name;
}
