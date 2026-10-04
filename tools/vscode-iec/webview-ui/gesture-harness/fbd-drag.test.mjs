// FBD editor drag gestures (F02, F07, F08, F11, F13): real CDP mouse
// down / move / up against the production bundle, asserting on the DOM
// and on the exact ops the webview posts. See INVENTORY.md.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, sleep, withPage, deliver, reset, center, esc, FBD, byId, node, fbdOps, rect } from './diagram-helpers.mjs';

recordClips('fbd-drag');

// A constant (editable literal) beside the usual diagram.
const FBD_K = {
	...FBD,
	nodes: [...FBD.nodes, { id: 'k:1', kind: 'input', label: '42', src: { line: 8 }, layer: 0, line: 8 }]
};

/** Real drag with several intermediate moves, optionally with a modifier held. */
async function drag(b, from, to, { steps = 8, modifiers = 0, hold = null } = {}) {
	if (hold) await b.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', ...hold });
	await b.moveTo(from.x, from.y);
	await b.send('Input.dispatchMouseEvent', { type: 'mousePressed', x: from.x, y: from.y, button: 'left', buttons: 1, clickCount: 1, modifiers });
	for (let i = 1; i <= steps; i++) {
		await b.send('Input.dispatchMouseEvent', {
			type: 'mouseMoved',
			x: from.x + ((to.x - from.x) * i) / steps,
			y: from.y + ((to.y - from.y) * i) / steps,
			button: 'left',
			buttons: 1,
			modifiers
		});
		await sleep(25);
	}
	await b.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: to.x, y: to.y, button: 'left', buttons: 0, clickCount: 1, modifiers });
	if (hold) await b.send('Input.dispatchKeyEvent', { type: 'keyUp', key: hold.key, code: hold.code, windowsVirtualKeyCode: hold.windowsVirtualKeyCode, modifiers: 0 });
	await sleep(250);
}
const SHIFT = { key: 'Shift', code: 'ShiftLeft', windowsVirtualKeyCode: 16, modifiers: 8 };

const selected = (b) =>
	b.eval(`[...document.querySelectorAll('.svelte-flow__node.selected')].map((n) => n.getAttribute('data-id')).sort()`);
const viewportT = (b) =>
	b.eval(`(() => { const m = new DOMMatrix(getComputedStyle(document.querySelector('.svelte-flow__viewport')).transform); return { x: m.e, y: m.f, k: m.a }; })()`);
const handle = (b, id, side, pin) => center(b, `${node(id)} .svelte-flow__handle.${side}[data-pin="${pin}"]`);
/** A point of empty canvas: scan the pane for one no node/edge/control covers. */
const emptyPoint = (b, near) =>
	b.eval(`(() => {
		const pane = document.querySelector('.svelte-flow__pane').getBoundingClientRect();
		const free = (x, y) => document.elementFromPoint(x, y)?.classList.contains('svelte-flow__pane');
		const [nx, ny] = ${JSON.stringify(near ?? [0, 0])};
		for (let r = 0; r < 600; r += 10)
			for (let a = 0; a < 360; a += 30) {
				const x = (nx || pane.left + 40) + r * Math.cos(a * Math.PI / 180), y = (ny || pane.top + 40) + r * Math.sin(a * Math.PI / 180);
				if (x > pane.left + 4 && x < pane.right - 4 && y > pane.top + 4 && y < pane.bottom - 4 && free(x, y)) return { x, y };
			}
		return null;
	})()`);

test('FBD drag: Shift+drag over empty canvas selects exactly the boxed nodes (F02)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		const a = await rect(b, node('cm:1'));
		const c = await rect(b, node('cm:2'));
		// Box the two lower notes (cm:1, cm:2) with a margin; start/finish on empty canvas.
		const from = { x: Math.min(a.x, c.x) - 6, y: Math.min(a.y, c.y) - 6 };
		const to = { x: Math.max(a.x + a.w, c.x + c.w) + 6, y: Math.max(a.y + a.h, c.y + c.h) + 6 };
		assert.ok(await b.eval(`document.elementFromPoint(${from.x}, ${from.y})?.classList.contains('svelte-flow__pane')`), 'box must start on empty canvas');
		// Expected = every node whose rect the box touches (xyflow's partial
		// selection mode), computed from the live DOM.
		const want = await b.eval(`[...document.querySelectorAll('.svelte-flow__node')].filter((n) => { const r = n.getBoundingClientRect(); return r.left < ${to.x} && r.right > ${from.x} && r.top < ${to.y} && r.bottom > ${from.y}; }).map((n) => n.getAttribute('data-id')).sort()`);
		assert.ok(want.length === 2 && want.includes('cm:1') && want.includes('cm:2'), JSON.stringify(want));
		await reset(b);
		await drag(b, from, to, { modifiers: 8, hold: SHIFT });
		assert.deepEqual(await selected(b), want);
		assert.deepEqual(await selected(b), ['cm:1', 'cm:2']);
		assert.deepEqual(await fbdOps(b), [], 'box-select edits nothing');
		// ...and the selection is real: Delete removes exactly those notes.
		await b.pressKey('Delete', { code: 'Delete', keyCode: 46 });
		await sleep(200);
		const ops = await fbdOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual([...ops[0].nodes].sort(), ['cm:1', 'cm:2']);
	});
});

test('FBD drag: Esc cancels a constant edit — no setLiteral, chip keeps its value (F07)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD_K, title: 'n.fbd' });
		const p = await center(b, byId('chip', 'k:1'));
		assert.ok(p, 'constant rendered');
		await reset(b);
		await b.dblclick(p.x, p.y);
		await sleep(250);
		assert.equal(await b.eval(`document.activeElement?.tagName`), 'INPUT', 'float editor should hold focus');
		await b.send('Input.insertText', { text: '99' });
		await sleep(100);
		await esc(b);
		await sleep(200);
		assert.equal(await b.eval(`document.activeElement?.tagName === 'INPUT'`), false, 'editor closed');
		assert.deepEqual(await fbdOps(b), []);
		assert.match(await b.eval(`document.querySelector(${JSON.stringify(node('k:1'))}).innerText`), /42/);
		assert.doesNotMatch(await b.eval(`document.querySelector(${JSON.stringify(node('k:1'))}).innerText`), /99/);
	});
});

test('FBD drag: dragging a node posts ONE setLayout per drag, with the new coordinates (F11)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		await sleep(300);
		const k = (await viewportT(b)).k; // fitView scale: flow units = screen / k
		const before = await rect(b, node('cm:2'));
		await reset(b);
		await drag(b, { x: before.cx, y: before.cy }, { x: before.cx + 60, y: before.cy + 40 });
		let ops = await fbdOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'setLayout');
		assert.equal(ops[0].entries.length, 1);
		const e1 = ops[0].entries[0];
		assert.equal(e1.node, 'cm:2');
		assert.ok(Number.isInteger(e1.x) && Number.isInteger(e1.y));
		const after = await rect(b, node('cm:2'));
		assert.ok(Math.abs(after.x - before.x - 60) < 10 && Math.abs(after.y - before.y - 40) < 10 && after.x > before.x + 30, JSON.stringify({ before, after }));
		// (xyflow starts the drag on the first move, so the node trails the cursor by about one step.)
		// A second drag posts another, further along.
		await drag(b, { x: after.cx, y: after.cy }, { x: after.cx + 50, y: after.cy });
		const after2 = await rect(b, node('cm:2'));
		ops = await fbdOps(b);
		assert.equal(ops.length, 2, JSON.stringify(ops));
		assert.equal(ops[1].type, 'setLayout');
		assert.equal(ops[1].entries[0].node, 'cm:2');
		assert.ok(Math.abs(ops[1].entries[0].x - e1.x - (after2.x - after.x) / k) < 2 && after2.x > after.x + 25, JSON.stringify([e1, ops[1].entries[0]]));
		assert.ok(Math.abs(ops[1].entries[0].y - e1.y) < 2);
	});
});

test('FBD drag: dragging empty canvas pans the viewport and posts no edit (F13)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		await sleep(400); // fitView settles
		const t0 = await viewportT(b);
		const from = await emptyPoint(b);
		assert.ok(from, 'found empty canvas');
		const to = { x: from.x + 70, y: from.y + 45 };
		assert.ok(await b.eval(`document.elementFromPoint(${to.x}, ${to.y}) != null`));
		await reset(b);
		await drag(b, from, to);
		const t1 = await viewportT(b);
		assert.ok(Math.abs(t1.x - t0.x - 70) < 4 && Math.abs(t1.y - t0.y - 45) < 4, JSON.stringify({ t0, t1 }));
		assert.equal(t1.k, t0.k, 'a pan does not zoom');
		assert.deepEqual(await fbdOps(b), []);
		assert.deepEqual(await selected(b), []);
	});
});

test('FBD drag: output pin → input pin posts a rewire naming exactly those pins (F08)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		const from = await handle(b, 'v:A', 'source', '');
		const to = await handle(b, 'b:c.Y', 'target', 'IN2');
		assert.ok(from && to, 'pins rendered');
		await reset(b);
		await drag(b, from, to);
		assert.deepEqual(await fbdOps(b), [{ type: 'rewire', to: 'b:c.Y', toPin: 'IN2', source: 'v:A', sourcePin: '' }]);
	});
});

test('FBD drag: dropping a wire on a block\'s + posts addInput (F08)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		const from = await handle(b, 'v:B', 'source', '');
		const to = await handle(b, 'b:c.Y', 'target', '+');
		assert.ok(from && to, 'pins rendered');
		await reset(b);
		await drag(b, from, to);
		assert.deepEqual(await fbdOps(b), [{ type: 'addInput', node: 'b:c.Y', source: 'v:B', sourcePin: '' }]);
	});
});

test('FBD drag: a wire released on empty canvas posts nothing (F08)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		const from = await handle(b, 'v:A', 'source', '');
		const to = await emptyPoint(b, [from.x + 20, from.y + 150]);
		await reset(b);
		await drag(b, from, to);
		assert.deepEqual(await fbdOps(b), []);
	});
});
