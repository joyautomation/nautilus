// The project tags a diagram's logic uses without declaring them. A
// nautilus PROGRAM has the manifest's tags (nautilus.yaml and its
// tag-files) in scope with no VAR_EXTERNAL block (#177/#210), so the vars
// panel lists them read-only beside the header's own declarations — a
// program written only against project tags otherwise looks empty there.
//
// Each editor contributes the identifiers its PROGRAM logic names (FB
// bodies name pins and locals, never tags), then usedProjectTags keeps the
// manifest tags among them that the header doesn't declare, matched
// case-insensitively as IEC names are.

import { argIdents, type LdElement, type LdModel } from './ladder.ts';
import type { FbdModel, VarDecl } from './layout.ts';
import type { SfcModel } from './sfc.ts';

export type ProjectTag = { name: string; type?: string; role?: string; unit?: string; desc?: string };

/** The manifest tags among `refs` that no header declaration (any scope)
 * and no `exclude` name shadows — in manifest order, each once. */
export function usedProjectTags(
	refs: Iterable<string>,
	tags: ProjectTag[] | undefined,
	declared: Pick<VarDecl, 'name'>[] | undefined,
	exclude: Iterable<string> = []
): ProjectTag[] {
	const want = new Set<string>();
	for (const r of refs) want.add(r.toLowerCase());
	const skip = new Set<string>();
	for (const v of declared ?? []) skip.add(v.name.toLowerCase());
	for (const x of exclude) skip.add(x.toLowerCase());
	const out: ProjectTag[] = [];
	const seen = new Set<string>();
	for (const t of tags ?? []) {
		const k = t.name.toLowerCase();
		if (want.has(k) && !skip.has(k) && !seen.has(k)) {
			seen.add(k);
			out.push(t);
		}
	}
	return out;
}

/** The variables a piece of ST names (an assignment element, a transition
 * condition, an action body), as base names: `Tank.Level` → `Tank`,
 * `Levels[i]` → `Levels`, `i`. An assignment's target counts; a call's pin
 * names (`tmr(PT := …)`) and the called name, members after `.`, typed
 * literals (`T#5S`), numbers, strings and comments do not. */
export function stIdents(text: string): string[] {
	const src = (text ?? '').replace(/\(\*[\s\S]*?\*\)|\/\/[^\n]*/g, ' ');
	const tok =
		/'[^']*'|"[^"]*"|[A-Za-z_][A-Za-z0-9_]*#[A-Za-z0-9_.:+-]*|\d[\w.#]*|\.\s*[A-Za-z_][A-Za-z0-9_]*|[A-Za-z_][A-Za-z0-9_]*|[()]/g;
	const out: string[] = [];
	const seen = new Set<string>();
	let depth = 0;
	for (let t: RegExpExecArray | null; (t = tok.exec(src)); ) {
		const w = t[0];
		if (w === '(') depth++;
		else if (w === ')') depth = Math.max(0, depth - 1);
		if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(w)) continue;
		const rest = src.slice(tok.lastIndex);
		// A called name (a function or an FB instance) is not a tag.
		if (/^\s*\(/.test(rest)) continue;
		// Inside a call, `name :=` / `name =>` is the callee's pin.
		if (depth > 0 && /^\s*(:=|=>)/.test(rest)) continue;
		const k = w.toLowerCase();
		if (!seen.has(k)) {
			seen.add(k);
			out.push(w);
		}
	}
	return out;
}

/** The names the ladder's PROGRAM rungs read or write: contact, coil and
 * function operands, block arguments and assignment bodies. A
 * FUNCTION_BLOCK's own rungs are skipped (their names are its pins and
 * locals), and so are the block instances rungs call (`exclude`). */
export function ldRefs(m: LdModel | null | undefined): { refs: string[]; exclude: string[] } {
	const refs: string[] = [];
	const exclude: string[] = [];
	const walk = (els: LdElement[] | undefined, program: boolean) => {
		for (const e of els ?? []) {
			if (e.inst) exclude.push(e.inst.replace(/\[.*$/, ''));
			if (e.trig) exclude.push(e.trig);
			if (program) {
				if (e.ref) refs.push(...argIdents(e.ref));
				if (e.args) refs.push(...argIdents(e.args));
				if (e.text) refs.push(...stIdents(e.text));
			}
			for (const leg of e.legs ?? []) walk(leg, program);
		}
	};
	for (const r of m?.rungs ?? []) {
		const program = !r.pou;
		walk(r.elements, program);
		walk(r.coils, program);
	}
	return { refs, exclude };
}

/** The names an FBD body's chips and coils carry. A FUNCTION_BLOCK body
 * has none to offer (its chips are pins and locals); block instances and
 * wire (connector) names are never tags. */
export function fbdRefs(m: FbdModel | null | undefined): { refs: string[]; exclude: string[] } {
	if (!m || m.block) return { refs: [], exclude: [] };
	const refs: string[] = [];
	const exclude: string[] = [];
	for (const n of m.nodes ?? []) {
		if (n.wire) exclude.push(n.wire);
		if (n.kind === 'fb') exclude.push(n.label);
		else if (n.kind === 'input' || n.kind === 'coil') refs.push(...argIdents(n.label ?? ''));
	}
	return { refs, exclude };
}

/** The names an SFC chart's action associations, transition conditions and
 * action bodies use. Step and action names are the chart's own, not tags. */
export function sfcRefs(m: SfcModel | null | undefined): { refs: string[]; exclude: string[] } {
	const refs: string[] = [];
	const exclude: string[] = [];
	for (const s of m?.steps ?? []) {
		exclude.push(s.name);
		for (const a of s.actions ?? []) refs.push(...stIdents(a.target ?? ''));
	}
	for (const a of m?.actions ?? []) exclude.push(a.name);
	for (const t of m?.trans ?? []) refs.push(...stIdents(t.cond ?? ''));
	for (const a of m?.actions ?? []) refs.push(...stIdents(a.body ?? ''));
	return { refs, exclude };
}
