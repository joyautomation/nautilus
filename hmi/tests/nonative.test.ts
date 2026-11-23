// The kit asks in dialogs of its own, never the browser's: a native prompt(),
// alert() or confirm() can be blocked or invisible (an embedded view, an
// automated browser), and then nothing happens and nobody is told (the
// Checkout page's Abandon run was one, #719). The one deliberate use is the
// confirm dialog's own fallback when no <ConfirmDialog/> is mounted.
import { describe, it, expect } from './harness.js';
// @ts-ignore the kit carries no @types/node: this spec runs on node
import { readdirSync, readFileSync, statSync } from 'node:fs';
// @ts-ignore
import { join, relative } from 'node:path';

// npm test runs from the kit's directory
declare const process: { cwd(): string };
const ROOT = join(process.cwd(), 'src/lib');
const ALLOWED = new Set(['confirm.svelte.ts']); // the fallback when nothing is mounted

function files(dir: string): string[] {
	const out: string[] = [];
	for (const name of readdirSync(dir) as string[]) {
		const p = join(dir, name);
		if (statSync(p).isDirectory()) out.push(...files(p));
		else if (/\.(svelte|ts)$/.test(name)) out.push(p);
	}
	return out;
}

/** A source with its comments and strings blanked, so prose and messages are not code. */
function code(src: string): string {
	return src
		.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
		.replace(/(^|[^:])\/\/.*$/gm, '$1')
		.replace(/(['"`])(?:\\.|(?!\1).)*\1/g, (m) => m[0] + ' '.repeat(Math.max(m.length - 2, 0)) + m[0]);
}

// bare prompt( / alert( (not a method of something else, not a member in a type like `{ prompt(): Promise<void> }`), or reached through window/globalThis
const NATIVE = /(?:^|[^.\w$])(?:prompt|alert)\s*\((?!\s*\)\s*:)|\b(?:window|globalThis)\s*\.\s*(?:prompt|alert|confirm)\b|\(globalThis[^)]*\)\s*\.\s*(?:prompt|alert|confirm)\b/;

describe('no native dialogs in the kit', () => {
	it('finds none outside the confirm dialog\'s own fallback', () => {
		const bad: string[] = [];
		for (const f of files(ROOT)) {
			if (ALLOWED.has(f.split('/').pop() as string)) continue;
			const lines = code(readFileSync(f, 'utf8')).split('\n');
			lines.forEach((l, i) => {
				if (NATIVE.test(l)) bad.push(`${relative(ROOT, f)}:${i + 1}: ${l.trim()}`);
			});
		}
		expect(bad).toEqual([]);
	});
	it('would catch one (the pattern itself)', () => {
		for (const s of ["const why = prompt('x');", 'alert("saved")', 'window.confirm("sure")', 'if (globalThis.prompt) {}', 'x = (globalThis as any).alert("a")']) expect(NATIVE.test(code(s))).toBe(true);
		for (const s of ['const confirm = () => 1;', 'await confirm({ title: "x" });', 'obj.prompt = 1;', "// prompt('x') is a comment", "const msg = 'alert(now)';", 'function alertUser() {}', 'let e: { prompt(): Promise<unknown> } | null = null;']) expect(NATIVE.test(code(s))).toBe(false);
	});
});
