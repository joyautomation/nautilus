// Edit a manifest's text where it changed and nowhere else (#711).
//
// Re-stringifying a parsed document does not round-trip: it re-wraps long
// flow maps, re-pads comments and collapses aligned columns, so every edit
// would diff the whole file. This module instead finds the node a change
// touches (through the `yaml` Document's source ranges) and replaces,
// inserts or removes just that text. An edit that changes nothing leaves
// the text byte-identical, and everything it does not name (comments,
// order, flow style, keys the form does not render) is untouched.

import { parseDocument, isMap, isSeq, isScalar, isPair, type Node, type YAMLMap, type YAMLSeq, type Pair } from 'yaml';
import type { Json } from './schema.js';

export type Path = (string | number)[];

export type Op =
	| { op: 'set'; path: Path; value: Json }
	| { op: 'delete'; path: Path }
	/** Add a row to the list at path, creating the list (a top-level key) when missing. */
	| { op: 'append'; path: Path; value: { [k: string]: Json } };

const WORDS = /^(true|false|yes|no|on|off|y|n|null|~)$/i;
const NUMBERISH = /^[-+]?(\.?\d[\d_]*\.?[\d_]*([eE][-+]?\d+)?|0x[0-9a-fA-F_]+|0o?[0-7_]+|\.inf|\.nan)$/i;
const PLAIN = /^[A-Za-z0-9_./][A-Za-z0-9_./:@+-]*$/;

/**
 * One scalar as manifest text. A string that either loader could read as
 * something else (yes, on, 0x10, 1e3, null) or that is not plainly safe is
 * double-quoted, so the Go loader and this one agree on its type.
 */
export function renderScalar(v: Json): string {
	if (v === null) return 'null';
	if (typeof v === 'boolean') return v ? 'true' : 'false';
	if (typeof v === 'number') {
		if (!Number.isFinite(v)) throw new Error('a number must be finite');
		return String(v);
	}
	if (typeof v !== 'string') throw new Error('not a scalar');
	if (v !== '' && PLAIN.test(v) && !WORDS.test(v) && !NUMBERISH.test(v)) return v;
	return JSON.stringify(v);
}

function key(k: string): string {
	return /^[A-Za-z_][A-Za-z0-9_-]*$/.test(k) && !WORDS.test(k) ? k : JSON.stringify(k);
}

/** A value in flow style: { a: 1, b: [x, y] }. */
export function renderFlow(v: Json): string {
	if (Array.isArray(v)) return `[${v.map(renderFlow).join(', ')}]`;
	if (v !== null && typeof v === 'object') {
		const e = Object.entries(v);
		return e.length ? `{ ${e.map(([k, x]) => `${key(k)}: ${renderFlow(x)}`).join(', ')} }` : '{}';
	}
	return renderScalar(v);
}

const lineStart = (t: string, i: number) => t.lastIndexOf('\n', i - 1) + 1;
function trimEnd(t: string, e: number, floor: number): number {
	while (e > floor && /\s/.test(t[e - 1])) e--;
	return e;
}
function lineEnd(t: string, i: number): number {
	const n = t.indexOf('\n', i);
	return n < 0 ? t.length : n;
}
const col = (t: string, i: number) => i - lineStart(t, i);

function splice(t: string, from: number, to: number, s: string): string {
	return t.slice(0, from) + s + t.slice(to);
}

function nodeRange(n: Node | null | undefined): [number, number] {
	const r = n?.range;
	if (!r) throw new Error('the manifest node has no source range');
	return [r[0], r[1]];
}

function parse(text: string) {
	const doc = parseDocument(text, { keepSourceTokens: true });
	if (doc.errors.length) throw new Error(`the manifest does not parse: ${doc.errors[0].message}`);
	return doc;
}

function isFlow(n: unknown): boolean {
	return (isMap(n) || isSeq(n)) && n.flow === true;
}

function pairOf(map: YAMLMap, k: string): Pair | undefined {
	return map.items.find((p) => isScalar(p.key) && String(p.key.value) === k) as Pair | undefined;
}

function parentAndLast(path: Path): [Path, string | number] {
	if (!path.length) throw new Error('an edit needs a path');
	return [path.slice(0, -1), path[path.length - 1]];
}

/** Apply ops in order; the text changes only where they say. */
export function patch(text: string, ops: Op[]): string {
	let t = text;
	for (const o of ops) t = apply(t, o);
	return t;
}

function apply(text: string, o: Op): string {
	const doc = parse(text);
	switch (o.op) {
		case 'set':
			return set(text, doc, o.path, o.value);
		case 'delete':
			return del(text, doc, o.path);
		case 'append':
			return append(text, doc, o.path, o.value);
	}
}

function sameValue(n: unknown, v: Json): boolean {
	if (isScalar(n)) return n.value === v || (v === null && n.value == null);
	if (isSeq(n) && Array.isArray(v)) return n.items.length === v.length && n.items.every((x, i) => sameValue(x, v[i]));
	if (isMap(n) && v !== null && typeof v === 'object' && !Array.isArray(v)) {
		const e = Object.entries(v);
		return n.items.length === e.length && e.every(([k, x]) => sameValue(pairOf(n, k)?.value, x));
	}
	return false;
}

function set(text: string, doc: ReturnType<typeof parse>, path: Path, value: Json): string {
	const existing = doc.getIn(path, true) as Node | undefined;
	if (existing) {
		if (sameValue(existing, value)) return text; // nothing changed: not a byte moves
		const [s, e] = nodeRange(existing);
		const scalar = value === null || typeof value !== 'object';
		return splice(text, s, trimEnd(text, e, s), scalar ? renderScalar(value) : renderFlow(value));
	}
	const [parentPath, k] = parentAndLast(path);
	const parent = (parentPath.length ? doc.getIn(parentPath, true) : doc.contents) as Node | undefined;
	if (!isMap(parent) || typeof k !== 'string') throw new Error(`cannot set ${path.join('.')}: its parent is not a map in the manifest`);
	return insertPair(text, parent, k, value);
}

function insertPair(text: string, parent: YAMLMap, k: string, value: Json): string {
	const rendered = value !== null && typeof value === 'object' ? renderFlow(value) : renderScalar(value);
	if (!parent.items.length) throw new Error('cannot add a key to an empty map in the manifest');
	const last = parent.items[parent.items.length - 1] as Pair;
	const lastNode = (last.value ?? last.key) as Node;
	const [, ve] = nodeRange(lastNode);
	if (parent.flow) {
		const e = trimEnd(text, ve, 0);
		return splice(text, e, e, `, ${key(k)}: ${rendered}`);
	}
	const first = nodeRange(parent.items[0].key as Node)[0];
	const e = trimEnd(text, ve, first);
	const at = lineEnd(text, e);
	return splice(text, at, at, `\n${' '.repeat(col(text, first))}${key(k)}: ${rendered}`);
}

function del(text: string, doc: ReturnType<typeof parse>, path: Path): string {
	const [parentPath, k] = parentAndLast(path);
	const parent = (parentPath.length ? doc.getIn(parentPath, true) : doc.contents) as Node | undefined;
	if (isMap(parent) && typeof k === 'string') {
		const idx = parent.items.findIndex((p) => isScalar(p.key) && String(p.key.value) === k);
		if (idx < 0) return text;
		return removeItem(text, parent, idx, true);
	}
	if (isSeq(parent) && typeof k === 'number') {
		if (k < 0 || k >= parent.items.length) throw new Error(`there is no row ${k} to delete`);
		// The last row takes its list's key with it: `devices:` left bare
		// would be a null where the loader expects a list.
		if (parent.items.length === 1 && parentPath.length) return del(text, doc, parentPath);
		return removeItem(text, parent, k, false);
	}
	throw new Error(`cannot delete ${path.join('.')}: it is not in the manifest`);
}

function removeItem(text: string, parent: YAMLMap | YAMLSeq, idx: number, isPairItem: boolean): string {
	const item = parent.items[idx];
	const startNode = (isPairItem ? (item as Pair).key : item) as Node;
	const endNode = (isPairItem ? ((item as Pair).value ?? (item as Pair).key) : item) as Node;
	const [s] = nodeRange(startNode);
	const [, ve] = nodeRange(endNode);
	const e = trimEnd(text, ve, s);
	if (parent.flow) {
		if (parent.items.length === 1) return splice(text, s, e, '');
		if (idx > 0) {
			const prevEnd = trimEnd(text, nodeRange((isPairItem ? ((parent.items[idx - 1] as Pair).value ?? (parent.items[idx - 1] as Pair).key) : parent.items[idx - 1]) as Node)[1], 0);
			return splice(text, prevEnd, e, '');
		}
		const nextStart = nodeRange((isPairItem ? (parent.items[1] as Pair).key : parent.items[1]) as Node)[0];
		return splice(text, s, nextStart, '');
	}
	// Block: the item's own lines. A row's text starts at its "- ", before
	// the node; a key's at the key. Refuse a first key sharing a line with
	// "- ": removing it would take the row's dash with it.
	const ls = lineStart(text, s);
	const before = text.slice(ls, s);
	let from = ls;
	if (isPairItem) {
		if (before.trim() !== '') throw new Error('cannot delete the first key of a row: delete the row');
	} else {
		if (!/^\s*-\s*$/.test(before)) throw new Error('cannot delete a row written on one line with other text');
	}
	const le = lineEnd(text, e);
	const to = le < text.length ? le + 1 : le;
	if (to === text.length && from > 0) from -= 1; // the last line: take the newline before it
	return splice(text, from, to, '');
}

function append(text: string, doc: ReturnType<typeof parse>, path: Path, row: { [k: string]: Json }): string {
	const seq = doc.getIn(path, true) as Node | undefined;
	if (!seq) {
		const [parentPath, k] = parentAndLast(path);
		const parent = (parentPath.length ? doc.getIn(parentPath, true) : doc.contents) as Node | undefined;
		if (!isMap(parent) || typeof k !== 'string') throw new Error(`cannot add to ${path.join('.')}: its parent is not a map in the manifest`);
		if (parent.flow || !parent.items.length) throw new Error('cannot add a list to this map in the manifest');
		const first = nodeRange(parent.items[0].key as Node)[0];
		const last = parent.items[parent.items.length - 1] as Pair;
		const e = trimEnd(text, nodeRange((last.value ?? last.key) as Node)[1], first);
		const at = lineEnd(text, e);
		const ind = ' '.repeat(col(text, first));
		return splice(text, at, at, `\n${ind}${key(k)}:\n${ind}  - ${renderFlow(row)}`);
	}
	if (!isSeq(seq)) throw new Error(`${path.join('.')} is not a list in the manifest`);
	if (!seq.items.length) throw new Error('cannot add a row to an empty list in the manifest');
	const lastItem = seq.items[seq.items.length - 1] as Node;
	const [ls, le] = nodeRange(lastItem);
	const e = trimEnd(text, le, ls);
	if (seq.flow) return splice(text, e, e, `, ${renderFlow(row)}`);
	const first = nodeRange(seq.items[0] as Node)[0];
	// The dash sits before the item on its line; rows copy the last row's
	// style: a flow map stays a flow map, a block map becomes a block map.
	const dashCol = col(text, lineStart(text, first) + text.slice(lineStart(text, first)).search(/-/));
	const ind = ' '.repeat(dashCol);
	let body: string;
	if (isFlow(lastItem) || !isMap(lastItem)) {
		body = `${ind}- ${renderFlow(row)}`;
	} else {
		const lines = Object.entries(row).map(([k, v]) => `${key(k)}: ${v !== null && typeof v === 'object' ? renderFlow(v) : renderScalar(v)}`);
		body = lines.map((l, i) => (i === 0 ? `${ind}- ${l}` : `${ind}  ${l}`)).join('\n');
	}
	const at = lineEnd(text, e);
	return splice(text, at, at, `\n${body}`);
}

export { isPair };
