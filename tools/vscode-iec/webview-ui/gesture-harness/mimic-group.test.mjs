// Several-item selections in the mimic editor (G01–G09): Shift-click builds
// one out of equipment and labels, a drag on any member moves them all with
// the pipes between them, Del deletes them all, a Shift-drag on empty canvas
// boxes items into it, and a Shift-press on an item adds it and drags. Real CDP input against the
// production bundle; see README "Adding a suite".
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, twoTankDoc, withEditor, sleepMs, exceptions } from './mimic-helpers.mjs';

recordClips('mimic-group');

const SHIFT = 8;
const CTRL = 2;

/** Two tanks with a bent pipe between them (ends on their walls, not
 * anchored) and a label. */
function groupDoc() {
	const d = twoTankDoc();
	d.pipes = [{ id: 'p1', points: [[240, 220], [420, 220], [420, 260], [600, 260]] }];
	d.labels = [{ text: 'LIT-101', x: 300, y: 100 }];
	return d;
}

const selIds = (ed) =>
	ed.b.eval("JSON.stringify(Array.from(document.querySelectorAll('.eq.sel, .lbl.sel')).map((e) => e.dataset.kind + ':' + e.dataset.id))").then(JSON.parse);
const at = (ed, kind, id) =>
	ed.b.eval(`(() => { const el = document.querySelector('[data-kind="${kind}"][data-id="${id}"]'); return JSON.stringify([el.offsetLeft, el.offsetTop]); })()`).then(JSON.parse);
/** Canvas units per viewport pixel. */
const unit = async (ed) => {
	const [a, b] = await Promise.all([ed.toVp(0, 0), ed.toVp(100, 0)]);
	return 100 / (b[0] - a[0]);
};
async function shiftClick(ed, kind, id, modifiers = SHIFT) {
	const r = await ed.locate(kind, id);
	await ed.b.click(r.cx, r.cy, { modifiers });
	await sleepMs(40);
}
/** Drag item (kind, id) by (dx, dy) canvas units; returns what was drawn mid-drag. */
async function dragBy(ed, kind, id, dx, dy, probe) {
	const r = await ed.locate(kind, id);
	const u = await unit(ed);
	await ed.b.mouseDown(r.cx, r.cy);
	await ed.b.mouseMove(r.cx + dx / u / 2, r.cy + dy / u / 2);
	await ed.b.mouseMove(r.cx + dx / u, r.cy + dy / u);
	await sleepMs(40);
	const live = probe ? await probe() : null;
	await ed.b.mouseUp(r.cx + dx / u, r.cy + dy / u);
	await sleepMs(80);
	return live;
}
const batchOps = async (ed) => {
	const ops = await ed.ops();
	assert.equal(ops.length, 1, `one op, got ${JSON.stringify(ops)}`);
	assert.equal(ops[0].type, 'batch');
	return ops[0].ops;
};

test('G01 Shift-click adds equipment and a label to the selection and takes one out again', async () => {
	await withEditor(groupDoc(), async (ed) => {
		await ed.selectEquipment('T1');
		await shiftClick(ed, 'equipment', 'T2');
		await shiftClick(ed, 'label', 0);
		assert.deepEqual((await selIds(ed)).sort(), ['equipment:T1', 'equipment:T2', 'label:0']);
		await shiftClick(ed, 'equipment', 'T1');
		assert.deepEqual((await selIds(ed)).sort(), ['equipment:T2', 'label:0']);
		await shiftClick(ed, 'label', 0, CTRL);
		assert.deepEqual(await selIds(ed), ['equipment:T2'], 'Ctrl-click still toggles, down to a single selection');
		assert.deepEqual(await ed.ops(), [], 'selecting posts nothing');
	});
});

test('G02 dragging one of the selection moves them all live, then posts ONE batch; the pipe between them moves whole', async () => {
	await withEditor(groupDoc(), async (ed) => {
		await ed.selectEquipment('T1');
		await shiftClick(ed, 'equipment', 'T2');
		await shiftClick(ed, 'label', 0);
		await ed.resetOps();
		const live = await dragBy(ed, 'equipment', 'T2', 50, 30, async () => [await at(ed, 'equipment', 'T1'), await at(ed, 'label', 0)]);
		assert.deepEqual(live, [[170, 210], [350, 130]], 'the other members follow mid-drag');
		const ops = await batchOps(ed);
		assert.deepEqual(ops, [
			{ type: 'moveEquipment', id: 'T1', x: 170, y: 210 },
			{ type: 'moveEquipment', id: 'T2', x: 650, y: 210 },
			{ type: 'updateLabel', index: 0, patch: { x: 350, y: 130 } },
			{ type: 'setPipePoints', id: 'p1', points: [[290, 250], [470, 250], [470, 290], [650, 290]] }
		]);
		assert.deepEqual((await selIds(ed)).sort(), ['equipment:T1', 'equipment:T2', 'label:0'], 'the selection survives the drag');
		assert.deepEqual(exceptions(ed), []);
	});
});

test('G03 a group move snaps the grabbed item to the grid and keeps the others where they are relative to it', async () => {
	const d = groupDoc();
	d.equipment[1].x = 603; // off-grid
	await withEditor(d, async (ed) => {
		await ed.selectEquipment('T1');
		await shiftClick(ed, 'equipment', 'T2');
		await ed.resetOps();
		await dragBy(ed, 'equipment', 'T1', 41, 0);
		const moves = (await batchOps(ed)).filter((o) => o.type === 'moveEquipment');
		assert.deepEqual(moves, [
			{ type: 'moveEquipment', id: 'T1', x: 160, y: 180 },
			{ type: 'moveEquipment', id: 'T2', x: 643, y: 180 }
		]);
	});
});

test('G04 a press and release on a member without moving selects just that one', async () => {
	await withEditor(groupDoc(), async (ed) => {
		await ed.selectEquipment('T1');
		await shiftClick(ed, 'label', 0);
		await ed.resetOps();
		const r = await ed.locate('label', 0);
		await ed.b.click(r.cx, r.cy);
		await sleepMs(40);
		assert.deepEqual(await selIds(ed), ['label:0']);
		assert.deepEqual(await ed.ops(), []);
	});
});

test('G05 Del deletes every member, labels included, in ONE batch', async () => {
	const d = groupDoc();
	d.labels.push({ text: 'FIT-101', x: 500, y: 100 });
	await withEditor(d, async (ed) => {
		await ed.selectEquipment('T1');
		await shiftClick(ed, 'label', 0);
		await shiftClick(ed, 'label', 1);
		await ed.resetOps();
		await ed.pressDelete();
		assert.deepEqual(await batchOps(ed), [
			{ type: 'deleteEquipment', id: 'T1' },
			{ type: 'deleteLabel', index: 1 },
			{ type: 'deleteLabel', index: 0 }
		]);
	});
});

/** Shift-drag from canvas point a to b; returns the selection mid-drag. */
async function shiftBox(ed, [ax, ay], [bx, by]) {
	const [x0, y0] = await ed.toVp(ax, ay);
	const [x1, y1] = await ed.toVp(bx, by);
	await ed.b.mouseDown(x0, y0, { modifiers: SHIFT });
	await ed.b.mouseMove((x0 + x1) / 2, (y0 + y1) / 2, { modifiers: SHIFT });
	await ed.b.mouseMove(x1, y1, { modifiers: SHIFT });
	await sleepMs(40);
	const mid = { sel: (await selIds(ed)).sort(), box: await ed.b.eval("!!document.querySelector('.selbox')") };
	await ed.b.mouseUp(x1, y1, { modifiers: SHIFT });
	await sleepMs(60);
	return mid;
}
const eqBottom = (ed, id) =>
	ed.b.eval(`(() => { const el = document.querySelector('.eq[data-id="${id}"]'); return el.offsetTop + el.offsetHeight; })()`);

test('G06 Shift-drag on empty canvas boxes in what lies wholly inside, added to the selection', async () => {
	await withEditor(groupDoc(), async (ed) => {
		await ed.selectEquipment('T2');
		await ed.resetOps();
		const bottom = await eqBottom(ed, 'T1');
		const mid = await shiftBox(ed, [90, 60], [380, bottom + 10]);
		assert.deepEqual(mid, { sel: ['equipment:T1', 'equipment:T2', 'label:0'], box: true }, 'the selection follows the box live');
		assert.deepEqual((await selIds(ed)).sort(), ['equipment:T1', 'equipment:T2', 'label:0']);
		assert.equal(await ed.b.eval("!!document.querySelector('.selbox')"), false, 'the box goes on release');
		assert.deepEqual(await ed.ops(), [], 'selecting posts nothing');
	});
});

test('G07 a box that only cuts through an item does not take it, and an empty box leaves the selection alone', async () => {
	await withEditor(groupDoc(), async (ed) => {
		await ed.selectEquipment('T2');
		const mid = await shiftBox(ed, [90, 60], [200, 300]); // half of T1, none of the label
		assert.deepEqual(mid.sel, ['equipment:T2']);
		assert.deepEqual(await selIds(ed), ['equipment:T2']);
	});
});

test('G08 Shift-click on empty canvas (no drag) still changes nothing', async () => {
	await withEditor(groupDoc(), async (ed) => {
		await ed.selectEquipment('T1');
		const [x, y] = await ed.toVp(60, 60);
		await ed.b.click(x, y, { modifiers: SHIFT });
		await sleepMs(40);
		assert.deepEqual(await selIds(ed), ['equipment:T1']);
	});
});

test('G09 Shift-press on an unselected item adds it, and dragging from that press moves the lot', async () => {
	await withEditor(groupDoc(), async (ed) => {
		await ed.selectEquipment('T1');
		await ed.resetOps();
		const r = await ed.locate('equipment', 'T2');
		const u = await unit(ed);
		await ed.b.mouseDown(r.cx, r.cy, { modifiers: SHIFT });
		await ed.b.mouseMove(r.cx + 15 / u, r.cy, { modifiers: SHIFT });
		await ed.b.mouseMove(r.cx + 30 / u, r.cy, { modifiers: SHIFT });
		await sleepMs(40);
		await ed.b.mouseUp(r.cx + 30 / u, r.cy, { modifiers: SHIFT });
		await sleepMs(80);
		const moves = (await batchOps(ed)).filter((o) => o.type === 'moveEquipment');
		assert.deepEqual(moves, [
			{ type: 'moveEquipment', id: 'T1', x: 150, y: 180 },
			{ type: 'moveEquipment', id: 'T2', x: 630, y: 180 }
		]);
		assert.deepEqual((await selIds(ed)).sort(), ['equipment:T1', 'equipment:T2'], 'both stay selected');
	});
});
