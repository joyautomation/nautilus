// Waiting on the page without guessing how long it takes (#545).
//
// A fixed sleep before an assertion holds on a fast box and fails on a
// loaded CI runner, where a render, a fit-to-view or a posted op can land
// later than any sleep picked on a laptop (#489, #545). These wait for the
// condition itself, up to a deadline, and fail there with what they saw.
// A fixed sleep still has one job: proving that something does NOT happen
// (no op posted, no second batch). Keep those; a poll can't prove absence.

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** Poll `probe()` until `ok(value)` holds; return that value. At the
 * deadline, throw naming `what` and the last value seen. */
export async function waitUntil(probe, ok = Boolean, { timeout = 5000, interval = 50, what = 'the condition' } = {}) {
	const until = Date.now() + timeout;
	for (;;) {
		const v = await probe();
		if (ok(v)) return v;
		if (Date.now() >= until) throw new Error(`waited ${timeout} ms for ${what}; last saw ${JSON.stringify(v)}`);
		await sleep(interval);
	}
}

/** Wait until at least `count` ops are posted (`get(b)` lists them, e.g.
 * sfcOps), then return them. The caller asserts on exactly what came back,
 * so "posts ONE op" still fails on a second one that is already there. */
export const opsPosted = (b, get, count = 1, opts = {}) =>
	waitUntil(() => get(b), (ops) => ops.length >= count, { what: `${count} posted op(s)`, ...opts });

// The page's layout in one string: where every drawn thing is, how far
// every pane is scrolled, how many elements there are.
const LAYOUT = `(() => {
	const parts = [document.querySelectorAll('*').length];
	for (const el of document.querySelectorAll('svg, canvas, [data-id], .svelte-flow__viewport')) {
		const r = el.getBoundingClientRect();
		parts.push(Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height));
	}
	for (const el of document.querySelectorAll('*')) if (el.scrollTop || el.scrollLeft) parts.push(el.scrollTop, el.scrollLeft);
	return parts.join(',');
})`;

/** Wait until the page has stopped moving: the layout is the same for
 * `frames` animation frames in a row. Covers a model render, a fit-to-view
 * a frame or two after it, a ResizeObserver rescale. Counts frames, not
 * milliseconds, so a slow runner just takes longer. */
export async function settled(b, { frames = 5, timeout = 5000 } = {}) {
	const ok = await b.eval(`new Promise((done) => {
		const layout = ${LAYOUT};
		const end = performance.now() + ${timeout};
		setTimeout(() => done(false), ${timeout + 250}); // no frames at all
		let last = layout(), still = 0;
		const tick = () => {
			const now = layout();
			if (now === last) { if (++still >= ${frames}) return done(true); }
			else { last = now; still = 0; }
			if (performance.now() > end) return done(false);
			requestAnimationFrame(tick);
		};
		requestAnimationFrame(tick);
	})`);
	if (!ok) throw new Error(`the page was still moving (or drew no frames) after ${timeout} ms`);
}
