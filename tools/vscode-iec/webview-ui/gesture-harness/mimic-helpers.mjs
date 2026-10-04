// Shared helpers and fixture docs for the mimic editor gesture suites
// (gestures.test.mjs and any sibling *.test.mjs). Importing this registers no
// tests. Bundle under test: env MIMIC_BUNDLE, else ../media/dist.
import assert from 'node:assert/strict';
import { Editor, applyOpToDoc, twoTankDoc } from './harness.mjs';
import { recordClips } from './clips.mjs';

export { recordClips, Editor, applyOpToDoc, twoTankDoc };
export const BUNDLE = process.env.MIMIC_BUNDLE || '../media/dist';
export const HEADLESS = process.env.HEADED !== '1';

// One fresh editor per test keeps app state (tool, selection, draft) clean.
export async function withEditor(doc, fn) {
	const ed = await Editor.open(BUNDLE, doc, { headless: HEADLESS });
	try {
		return await fn(ed);
	} finally {
		await ed.close();
	}
}

export const addPipe = (ops) => ops.find((o) => o.type === 'addPipe') ?? null;

// A doc with an existing anchored orthogonal pipe (T1.right -> floating end).
export function docWithPipe() {
	const d = twoTankDoc();
	d.pipes = [{ id: 'P1', points: [[400, 251], [400, 120]], from: { equip: 'T1', port: 'right' }, routing: 'orthogonal' }];
	return d;
}

// ── BUG (this ship): shift-click node multi-select + Delete ────────────────
// Three anchor shapes (unanchored, one anchor, both anchors) so the vtxDown
// interior-index conversion (`i - (p.from ? 1 : 0)`) and the terminal-handle
// exclusion are each exercised with and without an offsetting anchor handle
// at index 0. Every doc has >= 2 true INTERIOR points (so a node
// multi-selection has something to pick from — points[0]/points[last] are
// terminal handles regardless of anchoring, per vtxDown's isStart/isEnd).
export function docFloatingPipe() {
	const d = twoTankDoc();
	d.pipes = [{ id: 'P1', points: [[300, 200], [300, 300], [400, 300], [400, 400]] }];
	return d;
}

export function docFromAnchoredPipe() {
	const d = twoTankDoc();
	d.pipes = [{
		id: 'P1',
		points: [[300, 300], [300, 350], [420, 350], [420, 400]],
		from: { equip: 'T1', port: 'right' },
		routing: 'orthogonal'
	}];
	return d;
}

// Both ends anchored, autorouted-shaped (multiple interior corners) — the
// "both-anchored pipe (autorouted, multiple interior points)" case.
export function docBothAnchoredPipe() {
	const d = twoTankDoc();
	d.pipes = [{
		id: 'P1',
		points: [[300, 300], [300, 350], [420, 350]],
		from: { equip: 'T1', port: 'right' },
		to: { equip: 'T2', port: 'left' },
		routing: 'orthogonal'
	}];
	return d;
}

// ── visual assertions (this ship) ───────────────────────────────────────────
// Every prior assertion in this file reads DOM classes — `.vtx.sel` count,
// `ed.selection()`'s class-derived summary, etc. That proves the SELECTION
// STATE MACHINE picked the right node; it says nothing about what a `.sel`
// class actually PAINTS. BUG 3 (this ship): shift-clicking a pipe's terminal/
// anchor handle — an easy stray hit while multi-selecting nearby interior
// vertices, since it's a LARGER hit target sitting right next to them —
// unconditionally overwrote the whole node multi-selection with a plain
// `{kind:'end'}` one (vtxDown's terminal branch was the one shift-aware
// handler in the file that never got BUG 1's "shift-miss is a no-op" guard).
// Since a terminal handle's OWN dot never had any `.sel`-driven visual at all
// (a SEPARATE, related gap — `kind:'end'` isn't `kind:'nodes'`, so
// `nodeSelected()` never recognized it), the net effect read exactly like
// the user's report: click a vertex, the previously-highlighted dot(s)
// revert to their plain look with no data mutation — "the dot disappears".
// These assertions read `getComputedStyle`/`getBoundingClientRect` (via
// `ed.visualState()`) so a `.sel` rule that stops mattering — wrong layer,
// shadowed by a later same-specificity rule, a missing `class:sel` wire-up —
// fails a test even when the class list alone would have looked correct.

export function isVisiblyRendered(v) {
	const opacity = parseFloat(v.opacity);
	const fillNone = v.fill === 'none' && v.stroke === 'none';
	assert.ok(v.area > 0, `expected nonzero rendered area, got ${v.area} (classes="${v.classes}")`);
	assert.ok(opacity > 0, `expected opacity > 0, got ${v.opacity} (classes="${v.classes}")`);
	assert.notEqual(v.visibility, 'hidden', `expected visibility !== hidden (classes="${v.classes}")`);
	assert.ok(!fillNone, `expected a visible fill or stroke, got fill:none + stroke:none (classes="${v.classes}")`);
}

/** `sel` must carry the `sel` class, `base` must not, AND the two must
 * actually paint differently (fill/stroke/stroke-width) — the real cue a
 * "selected" class exists to add. Passing class checks alone (every other
 * assertion in this file) can't catch a `.sel` rule that stopped applying or
 * got shadowed by a same-specificity rule declared later in the stylesheet. */

export function assertVisuallyDistinctSelection(base, sel) {
	const hasSel = (v) => new RegExp('(^|\\s)sel(\\s|$)').test(v.classes);
	assert.ok(hasSel(sel), `expected the SELECTED element to carry the sel class (classes="${sel.classes}")`);
	assert.ok(!hasSel(base), `expected the baseline element to NOT carry the sel class (classes="${base.classes}")`);
	isVisiblyRendered(base);
	isVisiblyRendered(sel);
	const changed = base.fill !== sel.fill || base.stroke !== sel.stroke || base.strokeWidth !== sel.strokeWidth;
	assert.ok(
		changed,
		`expected fill/stroke/stroke-width to differ between selected and unselected — both computed identical (${JSON.stringify(sel)})`
	);
}

// ── clipboard, multi-select, shortcut help (editor parity) ────────────────
// Headless Chrome's clipboard is a stub, so these run on the in-webview
// fallback (readClip waits up to 400 ms on the system clipboard first).
export const sleepMs = (ms) => new Promise((r) => setTimeout(r, ms));

export async function ctrl(ed, k) {
	await ed.b.pressKey(k, { code: 'Key' + k.toUpperCase(), keyCode: k.toUpperCase().charCodeAt(0), modifiers: 2 });
	await sleepMs(500);
}

export async function selectBoth(ed) {
	await ed.selectEquipment('T1');
	const [, t2] = await ed.eqRects();
	await ed.b.click(t2.cx, t2.cy, { modifiers: 2 });
	await sleepMs(60);
}

export const eqSelCount = (ed) => ed.b.eval("document.querySelectorAll('.eq.sel').length");

export function docPipeBetween() {
	const d = twoTankDoc();
	d.equipment[0].props = { max: 50 };
	d.equipment[0].bind = { level: 'Level' };
	d.pipes = [{ id: 'P1', points: [[400, 251]], from: { equip: 'T1', port: 'right' }, to: { equip: 'T2', port: 'left' }, routing: 'orthogonal' }];
	return d;
}

// ── restore: mount with saved webview state ────────────────────────────────
// A restored panel (Developer: Reload Webviews, Reload Window) mounts with
// vscode.getState() holding whatever it last had. The mimic/component
// editors keep nothing there today — they re-handshake and the host
// re-sends the doc — but the mount must survive a non-empty state (an
// older build's, or a future one's) and still say ready, or the host never
// answers and the panel stays blank (the FBD editor's failure mode).
export const exceptions = (ed) => ed.console().filter((l) => l.startsWith('[exception]'));

export const STALE_STATE = { msg: { type: 'model', model: { name: 'x', nodes: [], edges: [] } }, zoom: { ld: 2 } };

// ── component editor (*.component.json) ────────────────────────────────────
// The mimic bundle also hosts the component editor (data-mimic-mode =
// "component"); `doc` is { component, ports }. Its ops are `componentOp`
// messages carrying the FULL ports list (not manifestOp / mimicOp).
export async function withComponent(doc, fn) {
	const ed = await Editor.open(BUNDLE, doc, { headless: HEADLESS, mode: 'component' });
	try {
		return await fn(ed);
	} finally {
		await ed.close();
	}
}

export const componentOps = (ed) =>
	ed.b.eval("JSON.stringify(window.__posted().filter((m) => m && m.type === 'componentOp').map((m) => m.ports))").then(JSON.parse);
