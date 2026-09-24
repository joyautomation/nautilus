// Browser gesture regression suite for the mimic editor webview — the layer
// the pure-logic tests (pipeDraft.test.ts, routing.test.ts …) can't reach:
// real focus, pointer capture, hit-testing, and (the recurring bug class)
// PREVIEW-vs-COMMIT geometry. Drives the PRODUCTION bundle in headless Chrome.
//
// Run: `npm run test:gestures` (from webview-ui). NOT part of `npm test` / CI —
// it needs a Chrome/Chromium on PATH. See gesture-harness/README.md.
//
// Bundle under test: argv/env MIMIC_BUNDLE, else ../media/dist (the repo build).
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { Editor, applyOpToDoc, twoTankDoc } from './harness.mjs';

const BUNDLE = process.env.MIMIC_BUNDLE || '../media/dist';
const HEADLESS = process.env.HEADED !== '1';

// One fresh editor per test keeps app state (tool, selection, draft) clean.
async function withEditor(doc, fn) {
	const ed = await Editor.open(BUNDLE, doc, { headless: HEADLESS });
	try {
		return await fn(ed);
	} finally {
		await ed.close();
	}
}

const addPipe = (ops) => ops.find((o) => o.type === 'addPipe') ?? null;

// A doc with an existing anchored orthogonal pipe (T1.right -> floating end).
function docWithPipe() {
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
function docFloatingPipe() {
	const d = twoTankDoc();
	d.pipes = [{ id: 'P1', points: [[300, 200], [300, 300], [400, 300], [400, 400]] }];
	return d;
}
function docFromAnchoredPipe() {
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
function docBothAnchoredPipe() {
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

test('shift-click node multi-select + Delete: UNANCHORED pipe removes exactly the selected interior points', async () => {
	// points [[300,200],[300,300],[400,300],[400,400]]: handles 0 and 3 are
	// terminal ('end') handles regardless of anchoring; 1 and 2 are the true
	// interior/multi-selectable points.
	let opsSeen;
	await withEditor(docFloatingPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(350, 300)));
		const handles = await ed.vtx();
		assert.equal(handles.length, 4);
		await ed.shiftClickVtx(handles[1]);
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 1 });
		await ed.shiftClickVtx(handles[2]);
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 2 });
		await ed.resetOps();
		await ed.pressDelete();
		opsSeen = await ed.ops();
	});
	const op = opsSeen.at(-1);
	assert.equal(op.type, 'setPipePoints');
	assert.deepEqual(op.points, [[300, 200], [400, 400]], 'only the two interior points are removed, terminals kept');
});

test('shift-click node multi-select + Delete: FROM-anchored pipe converts the FULL index to the right interior index', async () => {
	let opsSeen;
	await withEditor(docFromAnchoredPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(360, 350)));
		const handles = await ed.vtx();
		// handle 0 is the anchor (from T1.right); 1..3 interior; 4 the
		// unanchored terminal 'end'. Interior handles are 1 and 2 here.
		assert.equal(handles.length, 5);
		await ed.shiftClickVtx(handles[1]);
		await ed.shiftClickVtx(handles[2]);
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 2 });
		await ed.resetOps();
		await ed.pressDelete();
		opsSeen = await ed.ops();
	});
	const op = opsSeen.at(-1);
	assert.equal(op.type, 'setPipePoints');
	assert.deepEqual(op.points, [[420, 350], [420, 400]], 'the first two interior points are removed, the rest kept');
});

test('shift-click node multi-select + Delete: BOTH-anchored autorouted pipe', async () => {
	let opsSeen;
	await withEditor(docBothAnchoredPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(300, 325)));
		const handles = await ed.vtx();
		// 0 and 4 are the T1/T2 anchors; 1, 2, 3 the three interior corners.
		assert.equal(handles.length, 5);
		await ed.shiftClickVtx(handles[1]);
		await ed.shiftClickVtx(handles[3]);
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 2 });
		await ed.resetOps();
		await ed.pressDelete();
		opsSeen = await ed.ops();
	});
	const op = opsSeen.at(-1);
	assert.equal(op.type, 'setPipePoints');
	assert.deepEqual(op.points, [[300, 350]], 'the two ends of the 3-point interior run are removed, the middle stays');
});

// ── BUG 1 root cause (confirmed in THIS harness, deterministically — see
// gestures.test.mjs history for how the initially-suspected onCanvasCapture
// proximity handler and a Chrome-native-dblclick race were both ruled out
// first): vtxDown's shift branch is the ONLY selection-setting pointerdown
// handler that understands Shift. Every other one (canvasDown's empty-space
// fallback, pipeDown, midDown, eqDown, labelDown) used to set/clear
// `ed.selection` UNCONDITIONALLY — so a shift-click that MISSES a vertex
// handle (they're small; an easy miss while multi-selecting several) and
// lands on empty canvas, the pipe's own stroke, a midpoint square, equipment,
// or a label instead silently discarded the ENTIRE in-progress node
// multi-selection with no visual feedback. By the time Delete was pressed
// there was nothing left selected — "shift-select a few nodes, press
// Delete, nothing happens", reproduced 3/3 against the shipped 0.9.13
// bundle. Fixed: every one of those handlers now no-ops on a held Shift —
// Shift is reserved for vtxDown's toggle, everywhere else it's inert rather
// than a wipe. -->
test('shift-click MISSING a vertex (lands on empty canvas) does not clobber the node multi-selection', async () => {
	await withEditor(docBothAnchoredPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(300, 325)));
		const handles = await ed.vtx();
		await ed.shiftClickVtx(handles[1]);
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 1 });

		// Shift-click well clear of the pipe, any handle, and both tanks.
		await ed.b.click(...(await ed.toVp(500, 150)), { modifiers: 8 });
		await new Promise((r) => setTimeout(r, 60));
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 1 }, 'the miss must be a no-op, not a deselect');

		await ed.resetOps();
		await ed.pressDelete();
		const op = (await ed.ops()).at(-1);
		assert.equal(op.type, 'setPipePoints', 'Delete still has the node selection to act on');
	});
});

test('shift-click MISSING a vertex (lands on the pipe stroke itself) does not clobber the node multi-selection', async () => {
	await withEditor(docBothAnchoredPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(300, 325)));
		const handles = await ed.vtx();
		await ed.shiftClickVtx(handles[1]);
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 1 });

		// Shift-click the pipe's OWN stroke (its wide .hit path), not a handle.
		await ed.b.click(...(await ed.toVp(360, 350)), { modifiers: 8 });
		await new Promise((r) => setTimeout(r, 60));
		assert.deepEqual(
			await ed.selection(),
			{ kind: 'nodes', nodesCount: 1 },
			'must stay a nodes selection, not collapse to a whole-pipe one'
		);
	});
});

test('shift-click a node TWICE (toggle) deselects it and does NOT delete/touch the pipe', async () => {
	// A defensive hardening alongside the root cause above: two shift-clicks
	// at the SAME handle in quick succession CAN (timing-dependent — Chrome's
	// own click-merge detection) synthesize a native dblclick on the second
	// one, and the vtx circle's ondblclick (vtxDelete) used to run
	// UNCONDITIONALLY, ignoring the held Shift — which would silently mutate
	// the pipe out from under a multi-select. Fixed the same way: the
	// dblclick handler no-ops when e.shiftKey is held, so a same-vertex
	// double shift-click nets out to "toggled off", full stop, with the
	// pipe's points untouched (selection going to `none` once the last node
	// is toggled off is pre-existing/intended behavior — see vtxDown's shift
	// branch — not something this changes).
	await withEditor(docBothAnchoredPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(300, 325)));
		const handles = await ed.vtx();
		const before = await ed.pipePaths();
		await ed.resetOps();
		await ed.shiftDoubleClickVtx(handles[1]);
		assert.deepEqual(await ed.selection(), { kind: 'none' }, 'toggling the only selected node off clears the selection');
		assert.deepEqual(await ed.pipePaths(), before, 'the double-click must not mutate the pipe geometry');
		assert.deepEqual(await ed.ops(), [], 'no setPipePoints/deletePipe should have been posted by the dblclick');

		// Recovery: reselecting the pipe and shift-clicking builds a fresh
		// node selection normally — the toggle-to-null didn't wedge anything.
		await ed.clickPoint(...(await ed.toVp(300, 325)));
		const handles2 = await ed.vtx();
		await ed.shiftClickVtx(handles2[1]);
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 1 });
	});
});

// ── BUG (this ship): Enter completes a pipe whose end is a hovered port ──────

test('Enter completes a BENT pipe onto a hovered port (port not dropped)', async () => {
	// click T1.right, click a corner (empty), hover T2.left, Enter. Pre-fix the
	// draft already met the floor so the hovered port was dropped -> pipe landed
	// short of the port. Now it connects.
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPipeMode();
		await ed.clickPort(0, 'right');
		await ed.clickCanvas(...(await ed.toVp(400, 120)));
		await ed.hoverPort(1, 'left');
		await ed.resetOps();
		await ed.pressEnter();
		const ap = addPipe(await ed.ops());
		assert.ok(ap, 'a pipe should be created');
		assert.deepEqual(ap.to, { equip: 'T2', port: 'left' }, 'end must connect to the hovered port');
		assert.deepEqual(ap.from, { equip: 'T1', port: 'right' }, 'start stays anchored to T1.right');
	});
});

test('Enter completes a single-click pipe onto a hovered port', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPipeMode();
		await ed.clickCanvas(...(await ed.toVp(360, 300)));
		await ed.hoverPort(1, 'left');
		await ed.resetOps();
		await ed.pressEnter();
		const ap = addPipe(await ed.ops());
		assert.ok(ap, 'a pipe should be created');
		assert.deepEqual(ap.to, { equip: 'T2', port: 'left' });
	});
});

test('floating control: two clicks + Enter over empty canvas completes as clicked', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPipeMode();
		await ed.clickCanvas(...(await ed.toVp(300, 300)));
		await ed.clickCanvas(...(await ed.toVp(360, 300)));
		await ed.hoverAt(...(await ed.toVp(360, 430))); // drift over empty
		await ed.resetOps();
		await ed.pressEnter();
		const ap = addPipe(await ed.ops());
		assert.ok(ap);
		assert.equal(ap.to, undefined, 'no spurious port connection');
		assert.equal(ap.from, undefined);
	});
});

// ── PREVIEW == COMMIT: draw mode ────────────────────────────────────────────

test('draw preview == committed render (bent pipe to a port)', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPipeMode();
		await ed.clickPort(0, 'right');
		await ed.clickCanvas(...(await ed.toVp(400, 120)));
		await ed.hoverPort(1, 'left');
		const preview = (await ed.draft()).d;
		await ed.resetOps();
		await ed.pressEnter();
		const ap = addPipe(await ed.ops());
		await ed.setDoc(applyOpToDoc(twoTankDoc(), ap));
		const committed = (await ed.pipePaths())[0];
		assert.equal(preview, committed, 'the solid draft preview must equal the finished pipe');
	});
});

// ── PREVIEW == COMMIT: terminal & vertex drags ──────────────────────────────

test('terminal ATTACH: drag a floating end onto a port — preview == commit', async () => {
	await withEditor(docWithPipe(), async (ed) => {
		await ed.enterPipeMode();
		const t2left = await ed.portXY(1, 'left');
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(400, 185))); // select pipe
		const handles = await ed.vtx();
		const endH = handles[handles.length - 1];
		await ed.resetOps();
		const cap = await ed.dragAndCapture([endH.cx, endH.cy], [[t2left.cx - 60, t2left.cy - 30], [t2left.cx, t2left.cy]]);
		const op = (await ed.ops()).at(-1);
		assert.equal(op.type, 'updatePipe');
		assert.deepEqual(op.patch.to, { equip: 'T2', port: 'left' }, 'attaches to the hovered port');
		await ed.setDoc(applyOpToDoc(docWithPipe(), op));
		const committed = await ed.pipePaths();
		assert.deepEqual(cap.pipePaths, committed, 'mid-drag preview must equal the released pipe');
	});
});

test('terminal DETACH: drag an anchored end off its port — preview == commit', async () => {
	await withEditor(docWithPipe(), async (ed) => {
		// grab the anchored T1.right end by proximity (onCanvasCapture) — no
		// selection needed. Its viewport = the pipe path's first M coord.
		const m = (await ed.pipePaths())[0].match(/M\s+([\d.-]+)\s+([\d.-]+)/);
		const start = await ed.toVp(+m[1], +m[2]);
		const target = await ed.toVp(280, 400);
		await ed.resetOps();
		const cap = await ed.dragAndCapture(start, [[start[0] - 30, start[1] + 80], target]);
		const op = (await ed.ops()).at(-1);
		assert.equal(op.type, 'updatePipe');
		assert.equal(op.patch.from, null, 'detaches (from cleared)');
		await ed.setDoc(applyOpToDoc(docWithPipe(), op));
		const committed = await ed.pipePaths();
		assert.deepEqual(cap.pipePaths, committed, 'mid-drag preview must equal the released pipe');
	});
});

test('interior VERTEX drag — preview == commit', async () => {
	await withEditor(docWithPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(400, 185))); // select pipe
		const handles = await ed.vtx();
		// middle handle is the interior corner (index 1 of 3).
		const mid = handles[1];
		await ed.resetOps();
		const to = await ed.toVp(330, 300);
		const cap = await ed.dragAndCapture([mid.cx, mid.cy], [[mid.cx - 20, mid.cy + 20], to]);
		const op = (await ed.ops()).at(-1);
		assert.equal(op.type, 'setPipePoints');
		await ed.setDoc(applyOpToDoc(docWithPipe(), op));
		const committed = await ed.pipePaths();
		assert.deepEqual(cap.pipePaths, committed, 'mid-drag preview must equal the released pipe');
	});
});

// ── 0.9.11 features (shipped un-harness-verified) ───────────────────────────

test('clicking an anchored end selects the END, not the equipment underneath', async () => {
	await withEditor(docWithPipe(), async (ed) => {
		await ed.selectMode();
		const m = (await ed.pipePaths())[0].match(/M\s+([\d.-]+)\s+([\d.-]+)/);
		const anchor = await ed.toVp(+m[1], +m[2]);
		await ed.b.click(anchor[0], anchor[1]);
		await new Promise((r) => setTimeout(r, 60));
		// equipment must NOT be selected; the pipe's vertex handles must show
		// (only render for a pipe/end/nodes selection).
		const eqSel = await ed.b.eval("document.querySelectorAll('.eq.sel').length");
		const handles = await ed.vtx();
		assert.equal(eqSel, 0, 'equipment under the anchor must not be selected');
		assert.ok(handles.length > 0, 'the pipe end selection shows its vertex handles');
	});
});

// ── ports-edit arrow nudge (this ship): arrows nudge the SELECTED PORT, ──────
// never the equipment underneath, and do nothing when no port is selected.

test('ports-edit: ArrowRight nudges the selected port, equipment stays put', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPortsMode(0); // select T1, 'p' -> ports-edit
		const before = (await ed.portHandles()).find((p) => p.name === 'top'); // (0.5, 0) — not edge-clamped on x
		assert.ok(before, 'T1.top ports-edit dot should render');
		await ed.clickPortHandle('top');
		await ed.resetOps();
		await ed.pressArrow('right');
		const mops = await ed.manifestOps();
		const setPorts = mops.find((o) => o.type === 'setComponentPorts');
		assert.ok(setPorts, 'a setComponentPorts op should be posted (default ports-edit target is the shared sidecar)');
		const after = setPorts.ports.find((p) => p.name === 'top');
		assert.ok(after.x > before.fx, `port x should increase (was ${before.fx}, now ${after.x})`);
		assert.equal(after.y, before.fy, 'y is untouched by a horizontal nudge');
		const moveOps = (await ed.ops()).filter((o) => o.type === 'moveEquipment');
		assert.equal(moveOps.length, 0, 'the equipment itself must not move');
	});
});

test('ports-edit: Shift+ArrowRight steps by the grid, a plain arrow steps by one pixel', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPortsMode(0);
		const startX = (await ed.portHandles()).find((p) => p.name === 'top').fx;
		await ed.clickPortHandle('top');

		const nudgedX = async () => {
			const ports = (await ed.manifestOps()).find((o) => o.type === 'setComponentPorts')?.ports;
			return ports?.find((p) => p.name === 'top')?.x;
		};

		await ed.resetOps();
		await ed.pressArrow('right');
		const plainStep = (await nudgedX()) - startX;

		await ed.resetOps();
		await ed.pressArrow('right', { shift: true });
		const gridStep = (await nudgedX()) - startX;

		assert.ok(plainStep > 0, 'a plain arrow press moves the port right');
		// Shift steps by GRID (10) canvas pixels vs. 1 for a plain press — an
		// order of magnitude bigger, same convention as the equipment/pipe-end
		// nudges (GRID in mimicState.svelte.ts).
		assert.ok(gridStep > plainStep * 5, `Shift-arrow step (${gridStep}) should be roughly 10x the plain step (${plainStep})`);
	});
});

test('ports-edit: arrows do nothing when no port is selected — NOT the equipment-move fallthrough', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPortsMode(0); // no clickPortHandle — portsSelected stays null
		await ed.resetOps();
		await ed.pressArrow('right');
		await ed.pressArrow('down', { shift: true });
		const all = await ed.ops();
		const manifest = await ed.manifestOps();
		assert.equal(all.length, 0, 'no mimicOp (e.g. moveEquipment) posted');
		assert.equal(manifest.length, 0, 'no manifestOp (port nudge) posted either — arrows are swallowed, not forwarded');
	});
});

test('drag re-anchor: dragging a floating end onto a DIFFERENT port posts that anchor', async () => {
	await withEditor(docWithPipe(), async (ed) => {
		await ed.enterPipeMode();
		const t2left = await ed.portXY(1, 'left');
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(400, 185)));
		const handles = await ed.vtx();
		const endH = handles[handles.length - 1];
		await ed.resetOps();
		await ed.dragAndCapture([endH.cx, endH.cy], [[t2left.cx, t2left.cy]]);
		const op = (await ed.ops()).at(-1);
		assert.equal(op.type, 'updatePipe');
		assert.deepEqual(op.patch.to, { equip: 'T2', port: 'left' });
	});
});

// ── PORT-DOT Z-ORDER (this ship): a ports-edit dot sitting on the equipment's
// own edge used to be half-covered by the `.eq` box, painted after it in the
// DOM — the SAME z-order class BUG 2 fixed for pipe anchors. A dead-center
// click landed on the box (selecting/dragging the equipment) instead of the
// dot. Fixed the same way: the dots moved into their own svg layer painted
// AFTER the equipment boxes (portedit-handles, EditorCanvas.svelte).

test('ports-edit: a DEAD-CENTER click on a port dot selects the port, not the equipment underneath', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPortsMode(0);
		const before = (await ed.portHandles()).find((p) => p.name === 'top');
		assert.ok(before, 'T1.top ports-edit dot should render');
		// Dead center, no outward nudge — exactly the dot's own coordinates.
		await ed.b.click(before.cx, before.cy);
		await new Promise((r) => setTimeout(r, 60));
		const portSel = await ed.b.eval("document.querySelectorAll('circle.porthandle.sel').length");
		assert.equal(portSel, 1, 'the dead-center click must land on the port dot');

		// It must NOT have also grabbed the equipment (no drag/move posted by
		// a follow-up nudge — arrow should move the PORT, never the box).
		await ed.resetOps();
		await ed.pressArrow('right');
		const moveOps = (await ed.ops()).filter((o) => o.type === 'moveEquipment');
		assert.equal(moveOps.length, 0, 'the dead-center click selected the port, not the equipment');
		});
	});

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

function isVisiblyRendered(v) {
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
function assertVisuallyDistinctSelection(base, sel) {
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

test('VISUAL: shift-click a vertex — the dot stays visibly rendered AND visually distinct from an unselected one', async () => {
	await withEditor(docFloatingPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(350, 300)));
		const handles = await ed.vtx();
		await ed.shiftClickVtx(handles[1]);
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 1 });

		const states = await ed.visualState('.pipe-handles circle.vtx');
		assertVisuallyDistinctSelection(states[0], states[1]);

		// A second shift-click on a DIFFERENT vertex: both must now be
		// visibly selected and distinct from the still-unselected ones.
		await ed.shiftClickVtx(handles[2]);
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 2 });
		const states2 = await ed.visualState('.pipe-handles circle.vtx');
		assertVisuallyDistinctSelection(states2[0], states2[1]);
		assertVisuallyDistinctSelection(states2[3], states2[2]);
	});
});

test('VISUAL - BUG 3 root cause: shift-clicking a TERMINAL handle must not un-highlight an in-progress node multi-selection', async () => {
	await withEditor(docFloatingPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(350, 300)));
		const handles = await ed.vtx();
		await ed.shiftClickVtx(handles[1]);
		await ed.shiftClickVtx(handles[2]);
		assert.deepEqual(await ed.selection(), { kind: 'nodes', nodesCount: 2 });
		const before = await ed.visualState('.pipe-handles circle.vtx');
		assertVisuallyDistinctSelection(before[0], before[1]);
		assertVisuallyDistinctSelection(before[3], before[2]);

		// Shift-click handle 0 — a TERMINAL, not an interior vertex: the easy
		// miss BUG 3 covers (the terminal handle sits right next to the
		// interior ones being multi-selected).
		await ed.shiftClickVtx(handles[0]);
		assert.deepEqual(
			await ed.selection(),
			{ kind: 'nodes', nodesCount: 2 },
			'a shift-click missing onto the terminal handle must be a no-op, not clobber the node selection'
		);
		const after = await ed.visualState('.pipe-handles circle.vtx');
		assertVisuallyDistinctSelection(after[0], after[1]);
		assertVisuallyDistinctSelection(after[3], after[2]);
		assert.deepEqual(after[1], before[1], 'the already-selected dot must render IDENTICALLY before and after the miss');
		assert.deepEqual(after[2], before[2], 'the already-selected dot must render IDENTICALLY before and after the miss');

		// Delete must still act on the ORIGINAL two-node selection.
		await ed.resetOps();
		await ed.pressDelete();
		const op = (await ed.ops()).at(-1);
		assert.equal(op.type, 'setPipePoints');
		assert.deepEqual(op.points, [[300, 200], [400, 400]]);
	});
});

test('VISUAL: a plain (unanchored) terminal handle, selected outright (kind: end), shows its own selected visual', async () => {
	await withEditor(docFloatingPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(350, 300)));
		const handles = await ed.vtx();
		// Plain click (no shift) on the terminal handle -> {kind:'end'}.
		await ed.b.click(handles[0].cx, handles[0].cy);
		await new Promise((r) => setTimeout(r, 60));

		const states = await ed.visualState('.pipe-handles circle.vtx');
		assertVisuallyDistinctSelection(states[3], states[0]);
	});
});

test('VISUAL: an anchored terminal handle, selected outright (kind: end), shows its own selected visual (ring + center dot)', async () => {
	await withEditor(docFromAnchoredPipe(), async (ed) => {
		await ed.selectMode();
		await ed.clickPoint(...(await ed.toVp(360, 350)));
		const handles = await ed.vtx();
		// handle 0 is the FROM anchor (see the FROM-anchored test above).
		await ed.b.click(handles[0].cx, handles[0].cy);
		await new Promise((r) => setTimeout(r, 60));

		const ring = await ed.visualState('.pipe-handles circle.vtx.anchor');
		isVisiblyRendered(ring[0]);
		assert.match(ring[0].classes, /(^|\s)sel(\s|$)/, 'the selected anchor ring must carry the sel class');

		const dot = await ed.visualState('.pipe-handles circle.anchordot');
		isVisiblyRendered(dot[0]);
		assert.match(dot[0].classes, /(^|\s)sel(\s|$)/, 'the selected anchor center dot must carry the sel class');
	});
});

test('VISUAL: ports-edit dot selection (panel-row to dot) actually changes what paints, not just the class', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPortsMode(0);
		await ed.clickPortHandle('right');

		const states = await ed.visualState('.portedit-handles circle.porthandle');
		const selIdx = states.findIndex((s) => /(^|\s)sel(\s|$)/.test(s.classes));
		assert.notEqual(selIdx, -1, 'exactly one porthandle dot should carry .sel after clicking it');
		const baseIdx = states.findIndex((_, i) => i !== selIdx);
		assertVisuallyDistinctSelection(states[baseIdx], states[selIdx]);
	});
});

// ── invalid JSON: a stale canvas is locked, and a never-parsed doc offers
// the way out instead of "waiting for document…" ────────────────────────────
test('a parse error mid-edit locks the stale canvas: dragging equipment posts no op', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		const [t1] = await ed.eqRects();
		await ed.deliver({ type: 'mimicError', message: 'not valid JSON: Unexpected token' });
		await new Promise((r) => setTimeout(r, 60));
		assert.equal(await ed.b.eval("!!document.querySelector('.cols.locked[inert]')"), true, 'canvas area is locked');
		assert.match(await ed.b.eval("document.querySelector('.err')?.textContent ?? ''"), /Reopen as Text Editor/);
		await ed.resetOps();
		await ed.dragAndCapture([t1.cx, t1.cy], [[t1.cx + 60, t1.cy + 40]]);
		await ed.pressArrow('right');
		assert.deepEqual(await ed.ops(), [], 'no op reaches the host while the doc is broken');
		// The text parses again: the canvas unlocks and gestures work.
		await ed.deliver({ type: 'mimicDoc', doc: twoTankDoc(), title: 'harness.mimic.json' });
		await new Promise((r) => setTimeout(r, 60));
		assert.equal(await ed.b.eval("!!document.querySelector('.cols.locked')"), false);
		await ed.dragAndCapture([t1.cx, t1.cy], [[t1.cx + 60, t1.cy + 40]]);
		assert.ok((await ed.ops()).some((o) => o.type === 'moveEquipment'), 'drag commits once unlocked');
	});
});

test('a doc that never parsed shows the error and "Reopen as Text Editor", which asks the host', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		// Fresh editor state: simulate a first load that failed by clearing the
		// doc the harness delivered, then sending only the error.
		await ed.b.eval('location.reload(), true');
		for (let i = 0; i < 100; i++) {
			if (await ed.b.eval("!!document.querySelector('.editor')")) break;
			await new Promise((r) => setTimeout(r, 50));
		}
		await ed.deliver({ type: 'mimicError', message: 'not valid JSON: Unexpected end of input' });
		await new Promise((r) => setTimeout(r, 60));
		const text = await ed.b.eval("document.querySelector('.fatal')?.textContent ?? ''");
		assert.match(text, /Unexpected end of input/);
		assert.doesNotMatch(await ed.b.eval('document.body.textContent'), /waiting for document/);
		await ed.b.eval("document.querySelector('.fatal .reopen').click(), true");
		const posted = JSON.parse(await ed.b.eval('JSON.stringify(window.__posted().map((m) => m && m.type))'));
		assert.ok(posted.includes('reopenAsText'));
	});
});

test('the live pill reflects nautilus.liveValues.enabled and toggles it through the host', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		const pill = () => ed.b.eval("document.querySelector('button.live')?.textContent.trim() ?? ''");
		await ed.deliver({ type: 'mimicTags', tags: null, enabled: false });
		await new Promise((r) => setTimeout(r, 40));
		assert.match(await pill(), /live off/);
		await ed.deliver({ type: 'mimicTags', tags: null, enabled: true });
		await new Promise((r) => setTimeout(r, 40));
		assert.match(await pill(), /offline/);
		await ed.deliver({ type: 'mimicTags', tags: { LevelPct: 42 }, enabled: true });
		await new Promise((r) => setTimeout(r, 40));
		assert.match(await pill(), /● live/);
		await ed.resetOps();
		await ed.b.eval("document.querySelector('button.live').click(), true");
		const posted = JSON.parse(await ed.b.eval('JSON.stringify(window.__posted().map((m) => m && m.type))'));
		assert.deepEqual(posted, ['toggleLive']);
	});
});
