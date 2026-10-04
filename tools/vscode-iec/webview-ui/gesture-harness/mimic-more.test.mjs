// More mimic editor gestures: Esc (M05), equipment nudge (M08), Shift free
// angle while drawing a pipe (M15), undo/redo keys (M17), snap to grid (X38).
// Real CDP input against the production bundle; see README "Adding a suite".
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, twoTankDoc, withEditor, sleepMs } from './mimic-helpers.mjs';

recordClips('mimic-more');

const SHIFT = 8;
const esc = async (ed) => {
	await ed.b.pressKey('Escape', { code: 'Escape', keyCode: 27 });
	await sleepMs(60);
};
const eqIds = (ed) => ed.b.eval("Array.from(document.querySelectorAll('.eq.sel')).map((e) => e.dataset.id)");
const toolActive = (ed) => ed.b.eval("Array.from(document.querySelectorAll('.tools button.active')).map((b) => b.textContent.trim())");
const moves = async (ed) => (await ed.ops()).filter((o) => o.type === 'moveEquipment');

// ── M05 Esc ────────────────────────────────────────────────────────────────
test('M05 Esc in pipe mode with a draft in progress discards it, returns to Select, posts nothing', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPipeMode();
		await ed.resetOps();
		await ed.clickPoint(...(await ed.toVp(400, 100)));
		await ed.clickPoint(...(await ed.toVp(500, 100)));
		assert.equal((await ed.draft()).count, 2, 'two draft points placed');
		assert.ok((await toolActive(ed)).some((l) => l.includes('Pipe')), 'Pipe is the active tool');
		await esc(ed);
		const d = await ed.draft();
		assert.equal(d.count, 0, 'draft points are gone');
		assert.equal(d.d, null, 'rubber-band path is gone');
		assert.deepEqual(await ed.ops(), [], 'Esc posts no op');
		// First Esc only cancels the draft; the tool is still Pipe. A second
		// Esc returns to Select (the draft-then-tool two-step in onkeydown).
		await esc(ed);
		const active = await toolActive(ed);
		assert.ok(active.some((l) => l === 'Select'), `Select is the active tool, got ${JSON.stringify(active)}`);
		// And a canvas click no longer starts a draft.
		await ed.clickPoint(...(await ed.toVp(400, 100)));
		assert.equal((await ed.draft()).count, 0);
		assert.deepEqual(await ed.ops(), []);
	});
});

test('M05 Esc in the ports editor leaves it (equipment stays selected)', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPortsMode('T1');
		assert.ok((await ed.portHandles()).length > 0, 'ports editor shows its dots');
		await ed.resetOps();
		await esc(ed);
		assert.equal((await ed.portHandles()).length, 0, 'ports editor closed');
		assert.deepEqual(await eqIds(ed), ['T1'], 'leaving the ports editor keeps the selection');
		assert.deepEqual(await ed.ops(), []);
	});
});

test('M05 Esc with equipment selected deselects it', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.selectEquipment('T1');
		assert.deepEqual(await eqIds(ed), ['T1']);
		await ed.resetOps();
		await esc(ed);
		assert.deepEqual(await eqIds(ed), []);
		assert.deepEqual(await ed.ops(), []);
	});
});

// ── M08 equipment nudge ────────────────────────────────────────────────────
test('M08 ArrowRight nudges the selected equipment by one pixel, Shift+ArrowRight by one grid step', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.selectEquipment('T1');
		await ed.resetOps();
		await ed.pressArrow('right');
		let m = await moves(ed);
		assert.equal(m.length, 1);
		assert.deepEqual(m[0], { type: 'moveEquipment', id: 'T1', x: 121, y: 180 });

		await ed.resetOps();
		await ed.pressArrow('right', { shift: true });
		m = await moves(ed);
		assert.equal(m.length, 1);
		// The doc was not reflected back, so the step is from the original x.
		assert.deepEqual(m[0], { type: 'moveEquipment', id: 'T1', x: 130, y: 180 });
	});
});

// ── M15 Shift = free angle ─────────────────────────────────────────────────
// The rubber-band path ends at the cursor; the COMMITTED draft points are what
// we assert on, via the committed segment's end in the draft path.
const draftNums = (d) => (d.d.match(/-?\d+(?:\.\d+)?/g) ?? []).map(Number);

test('M15 without Shift the second pipe point is orthogonalised to H/V', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPipeMode();
		await ed.clickPoint(...(await ed.toVp(400, 100)));
		await ed.clickPoint(...(await ed.toVp(500, 160)));
		const d = await ed.draft();
		assert.equal(d.count, 2);
		const n = draftNums(d);
		// Dominant axis is x, so y is held at the first point's 100.
		assert.deepEqual(n.slice(0, 4), [400, 100, 500, 100], `draft ${d.d}`);
	});
});

test('M15 holding Shift while placing a pipe point keeps the free diagonal', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPipeMode();
		await ed.clickPoint(...(await ed.toVp(400, 100)));
		await ed.b.click(...(await ed.toVp(500, 160)), { modifiers: SHIFT });
		await sleepMs(40);
		const d = await ed.draft();
		assert.equal(d.count, 2);
		const n = draftNums(d);
		assert.deepEqual(n.slice(0, 4), [400, 100, 500, 160], `draft ${d.d}`);
	});
});

// ── M17 Undo / redo ────────────────────────────────────────────────────────
// The webview owns no undo stack: every op is one WorkspaceEdit applied by the
// host (src/mimicEditor.ts), so VS Code's own text undo/redo covers it. The
// editor must therefore post nothing for the keys AND not swallow them
// (preventDefault) so VS Code still receives them.
test('M17 Ctrl+Z / Ctrl+Y / Ctrl+Shift+Z post no message and are left for VS Code', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.selectEquipment('T1');
		await ed.pressArrow('right'); // something to undo
		await ed.b.eval(`window.__keyPrevented = []; window.addEventListener('keydown', (e) => setTimeout(() => window.__keyPrevented.push(e.defaultPrevented), 0)); true`);
		await ed.resetOps();
		await ed.b.pressKey('z', { code: 'KeyZ', keyCode: 90, modifiers: 2 });
		await ed.b.pressKey('y', { code: 'KeyY', keyCode: 89, modifiers: 2 });
		await ed.b.pressKey('z', { code: 'KeyZ', keyCode: 90, modifiers: 2 | SHIFT });
		await sleepMs(80);
		assert.deepEqual(await ed.b.eval('JSON.stringify(window.__posted())').then(JSON.parse), [], 'no host message for undo/redo');
		assert.deepEqual(await ed.b.eval('window.__keyPrevented'), [false, false, false], 'keys are not swallowed by the webview');
	});
});

// ── X38 Snap to grid ───────────────────────────────────────────────────────
async function dragT1By(ed, dx, dy) {
	await ed.selectMode();
	const r = await ed.locate('equipment', 'T1');
	// dx/dy are canvas units; the viewport scales the canvas, so convert.
	const [x0, y0] = await ed.toVp(0, 0);
	const [x1, y1] = await ed.toVp(100, 100);
	const vx = ((x1 - x0) / 100) * dx;
	const vy = ((y1 - y0) / 100) * dy;
	await ed.resetOps();
	await ed.b.mouseDown(r.cx, r.cy);
	await sleepMs(30);
	await ed.b.mouseMove(r.cx + vx / 2, r.cy + vy / 2);
	await sleepMs(30);
	await ed.b.mouseMove(r.cx + vx, r.cy + vy);
	await sleepMs(30);
	await ed.b.mouseUp(r.cx + vx, r.cy + vy);
	await sleepMs(60);
	return moves(ed);
}

test('X38 snapToGrid on: an off-grid drag lands on the 10 px grid', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.deliver({ type: 'mimicConfig', snapToGrid: true });
		await sleepMs(60);
		const m = await dragT1By(ed, 33, 27);
		assert.equal(m.length, 1);
		assert.equal(m[0].x % 10, 0, `x ${m[0].x} on grid`);
		assert.equal(m[0].y % 10, 0, `y ${m[0].y} on grid`);
		assert.deepEqual([m[0].x, m[0].y], [150, 210], 'x 153 rounds down, y 207 rounds up');
	});
});

test('X38 snapToGrid off: the same drag moves by the exact delta', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.deliver({ type: 'mimicConfig', snapToGrid: false });
		await sleepMs(60);
		const m = await dragT1By(ed, 33, 27);
		assert.equal(m.length, 1);
		assert.deepEqual([m[0].x, m[0].y], [153, 207]);
	});
});
