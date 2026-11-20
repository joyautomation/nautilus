// What a control may do, judged the way the controller judges it (#708 PR
// 7b), from GET /api/me: the permissions held everywhere (`*`) and in each
// area, and each area's tag patterns. No runes, no DOM; tested in
// tests/areas.test.ts against the same table (tests/areamatch.json) the
// controller's own matcher is tested against (auth/areamatch_test.go), so the
// two cannot drift.
//
// It is a CONVENIENCE: a control greyed out here would have been refused, and
// one left enabled may still be refused. The controller decides every request.

import type { Me } from './auth.js';

/**
 * The tag an action's target names, as the controller cuts it: lowercased,
 * trimmed, without a member (`.x`) or an index (`[3]`).
 */
export function tagOf(target: string): string {
	let tag = target.toLowerCase();
	const i = tag.search(/[.[]/);
	if (i >= 0) tag = tag.slice(0, i);
	return tag.trim();
}

// ── Go's path.Match, rune for rune ──────────────────────────────────────────
// Ported from the Go standard library (path/match.go, BSD licence) so the
// kit's answer is the controller's: `*` and `?` never match a '/', `[...]`
// classes with ranges and `^`, `\` escapes, and a malformed pattern matches
// nothing.

type Chunk = { star: boolean; chunk: string[]; rest: string[] };

function scanChunk(p: string[]): Chunk {
	let star = false;
	while (p.length > 0 && p[0] === '*') {
		p = p.slice(1);
		star = true;
	}
	let inrange = false;
	let i = 0;
	scan: for (; i < p.length; i++) {
		switch (p[i]) {
			case '\\':
				if (i + 1 < p.length) i++;
				break;
			case '[':
				inrange = true;
				break;
			case ']':
				inrange = false;
				break;
			case '*':
				if (!inrange) break scan;
				break;
		}
	}
	return { star, chunk: p.slice(0, i), rest: p.slice(i) };
}

class BadPattern extends Error {}

function getEsc(chunk: string[]): { r: string; rest: string[] } {
	if (chunk.length === 0 || chunk[0] === '-' || chunk[0] === ']') throw new BadPattern();
	if (chunk[0] === '\\') {
		chunk = chunk.slice(1);
		if (chunk.length === 0) throw new BadPattern();
	}
	const r = chunk[0];
	const rest = chunk.slice(1);
	if (rest.length === 0) throw new BadPattern();
	return { r, rest };
}

const cp = (r: string) => r.codePointAt(0) ?? 0;

/** Match chunk against the start of s: the rest of s, or null. Throws on a bad pattern. */
function matchChunk(chunk: string[], s: string[]): string[] | null {
	let failed = false;
	while (chunk.length > 0) {
		if (!failed && s.length === 0) failed = true;
		const c = chunk[0];
		if (c === '[') {
			let r = '';
			if (!failed) {
				r = s[0];
				s = s.slice(1);
			}
			chunk = chunk.slice(1);
			let negated = false;
			if (chunk.length > 0 && chunk[0] === '^') {
				negated = true;
				chunk = chunk.slice(1);
			}
			let match = false;
			let nrange = 0;
			for (;;) {
				if (chunk.length > 0 && chunk[0] === ']' && nrange > 0) {
					chunk = chunk.slice(1);
					break;
				}
				const lo = getEsc(chunk);
				chunk = lo.rest;
				let hi = lo.r;
				if (chunk[0] === '-') {
					const h = getEsc(chunk.slice(1));
					hi = h.r;
					chunk = h.rest;
				}
				if (!failed && cp(lo.r) <= cp(r) && cp(r) <= cp(hi)) match = true;
				nrange++;
			}
			if (match === negated) failed = true;
		} else if (c === '?') {
			if (!failed) {
				if (s[0] === '/') failed = true;
				s = s.slice(1);
			}
			chunk = chunk.slice(1);
		} else {
			if (c === '\\') {
				chunk = chunk.slice(1);
				if (chunk.length === 0) throw new BadPattern();
			}
			if (!failed) {
				if (chunk[0] !== s[0]) failed = true;
				s = s.slice(1);
			}
			chunk = chunk.slice(1);
		}
	}
	return failed ? null : s;
}

/** Go's path.Match(pattern, name); a malformed pattern is false. */
export function pathMatch(pattern: string, name: string): boolean {
	try {
		return match(Array.from(pattern), Array.from(name));
	} catch (e) {
		if (e instanceof BadPattern) return false;
		throw e;
	}
}

function match(pattern: string[], name: string[]): boolean {
	outer: while (pattern.length > 0) {
		const { star, chunk, rest } = scanChunk(pattern);
		pattern = rest;
		if (star && chunk.length === 0) return !name.includes('/');
		const t = matchChunk(chunk, name);
		if (t !== null && (t.length === 0 || pattern.length > 0)) {
			name = t;
			continue;
		}
		if (star) {
			for (let i = 0; i < name.length && name[i] !== '/'; i++) {
				const t2 = matchChunk(chunk, name.slice(i + 1));
				if (t2 !== null) {
					if (pattern.length === 0 && t2.length > 0) continue;
					name = t2;
					continue outer;
				}
			}
		}
		// Before saying no, the rest of the pattern must be well formed.
		while (pattern.length > 0) {
			const next = scanChunk(pattern);
			pattern = next.rest;
			matchChunk(next.chunk, []);
		}
		return false;
	}
	return name.length === 0;
}

/** Whether an area's tag pattern takes in an action's target, as the controller decides it. */
export function areaMatch(pattern: string, target: string): boolean {
	return pathMatch(pattern.toLowerCase(), tagOf(target));
}

/**
 * Whether `me` may do `perm` — on `target` (a tag, member path or element)
 * when given, else on the whole controller. Without an auth: block (or no
 * /api/me) everything is allowed, as before. A target that names no tag
 * (".x", "[0]", a pattern: any of * ? [ ] \) is refused, as the controller
 * refuses it.
 */
export function can(me: Me | null | undefined, perm: string, target?: string): boolean {
	if (!me || !me.auth) return true;
	const everywhere = me.permissions?.['*']?.includes(perm) ?? false;
	if (target === undefined) return everywhere;
	const tag = tagOf(target);
	if (tag === '' || /[*?[\]\\]/.test(tag)) return false; // as Policy.Can: * ? [ ] \
	if (everywhere) return true;
	for (const [area, perms] of Object.entries(me.permissions ?? {})) {
		if (area === '*' || !perms.includes(perm)) continue;
		if ((me.areas?.[area] ?? []).some((p) => areaMatch(p, target))) return true;
	}
	return false;
}

/** Who `me` is, for a sentence: the user's name, `a token`, or `not signed in`. */
export function whoIs(me: Me | null | undefined): string {
	const p = me?.principal ?? 'anonymous';
	if (p === 'anonymous') return 'not signed in';
	if (p.startsWith('user:')) return p.slice(5);
	return p === 'token' ? 'the controller token' : `token ${p.slice(6)}`;
}

/**
 * Why a control is greyed out: who, and what they do not hold. Says the
 * controller decides, because it does.
 */
export function deniedReason(me: Me | null | undefined, perm: string, target?: string): string {
	const who = whoIs(me);
	if (who === 'not signed in') return `sign in to ${perm}${target ? ` ${target}` : ''}`;
	const roles = me?.roles?.length ? ` (${me.roles.join(', ')})` : '';
	return `${who}${roles} does not hold ${perm}${target ? ` on ${target}` : ''}`;
}
