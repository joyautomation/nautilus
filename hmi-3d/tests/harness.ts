// A ~60-line test harness, so the kit's pure logic can have tests without
// the kit acquiring a test runner.
//
// The kit is a component library: almost everything in it is a Svelte
// component whose "test" is looking at it, and pulling in vitest + a DOM
// implementation to assert on rendered SVG would be a large dependency for
// a small return. But the kit also contains a little pure logic that can
// silently mislead an operator if it is wrong — the delta-frame merge above
// all — and that deserves real tests.
//
// So: the same `describe` / `it` / `expect` surface vitest exposes, over
// plain objects, with no dependencies. `npm test` bundles the specs with
// esbuild (already present, via vite) and runs them on node. If the kit
// ever does adopt vitest, every spec keeps working by changing one import
// line per file — the API here is deliberately a strict subset.

// The kit is a browser library and deliberately carries no @types/node;
// pulling them in for one exit hook would add a dependency the published
// package has no use for. Declare exactly the sliver this file uses.
declare const process: {
	exitCode?: number;
	on(event: 'exit', listener: () => void): void;
};

type Fn = () => void | Promise<void>;

let failures = 0;
let ran = 0;
let pending = 0;

export function describe(name: string, fn: () => void): void {
	console.log(name);
	fn();
}

function pass(name: string) {
	console.log(`  ok   ${name}`);
}
function fail(name: string, e: unknown) {
	failures++;
	console.log(`  FAIL ${name}\n       ${(e as Error)?.message ?? String(e)}`);
}

/**
 * A spec may be async. Anything it awaits must be MICROTASK-only (a promise
 * already in flight, not a timer): the harness has no runner loop, it simply
 * lets node drain the microtask queue before `exit` fires. A spec that awaits
 * a timer would be reported as never having settled rather than silently
 * passing — see the `pending` check in the exit hook.
 */
export function it(name: string, fn: Fn): void {
	ran++;
	try {
		const r = fn();
		if (r && typeof (r as Promise<void>).then === 'function') {
			pending++;
			(r as Promise<void>).then(
				() => {
					pending--;
					pass(name);
				},
				(e) => {
					pending--;
					fail(name, e);
				}
			);
			return;
		}
		pass(name);
	} catch (e) {
		fail(name, e);
	}
}

const show = (v: unknown) => {
	try {
		return JSON.stringify(v) ?? String(v);
	} catch {
		return String(v);
	}
};

// Structural equality over the JSON-shaped values these specs deal in.
// Key ORDER must not matter — a merged tag map and the map it is compared
// against are built by different routes.
function deepEqual(a: unknown, b: unknown): boolean {
	if (Object.is(a, b)) return true;
	if (typeof a !== 'object' || typeof b !== 'object' || a === null || b === null) return false;
	if (Array.isArray(a) !== Array.isArray(b)) return false;
	const ak = Object.keys(a as object);
	const bk = Object.keys(b as object);
	if (ak.length !== bk.length) return false;
	for (const k of ak) {
		if (!Object.prototype.hasOwnProperty.call(b, k)) return false;
		if (!deepEqual((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]))
			return false;
	}
	return true;
}

export interface Matchers {
	toBe(want: unknown): void;
	toEqual(want: unknown): void;
	toBeNull(): void;
	readonly not: Pick<Matchers, 'toBe' | 'toEqual'>;
}

export function expect(got: unknown): Matchers {
	return {
		toBe(want) {
			if (!Object.is(got, want)) throw new Error(`toBe: got ${show(got)}, want ${show(want)}`);
		},
		toEqual(want) {
			if (!deepEqual(got, want)) throw new Error(`toEqual: got ${show(got)}, want ${show(want)}`);
		},
		toBeNull() {
			if (got !== null) throw new Error(`toBeNull: got ${show(got)}`);
		},
		get not() {
			return {
				toBe(want: unknown) {
					if (Object.is(got, want)) throw new Error(`not.toBe: both are ${show(got)}`);
				},
				toEqual(want: unknown) {
					if (deepEqual(got, want)) throw new Error(`not.toEqual: both are ${show(got)}`);
				}
			};
		}
	};
}

// Reported at exit rather than after each describe, so a run of several
// spec files gives one verdict and a non-zero status CI can read.
process.on('exit', () => {
	if (pending > 0) {
		// An async spec that never settled is a spec that never ran. Report it
		// as a failure rather than letting it disappear into a green run.
		failures += pending;
		console.log(`\n  FAIL ${pending} async spec(s) never settled`);
	}
	console.log(`\n${ran - failures}/${ran} passed`);
	if (failures > 0) process.exitCode = 1;
});
