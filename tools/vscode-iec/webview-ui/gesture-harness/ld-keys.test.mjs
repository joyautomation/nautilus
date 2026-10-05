// Ladder editor gestures with no coverage elsewhere (INVENTORY L07, L09,
// L10, L17, L19): single-key edits, middle-drag pan, redo, and element moves
// asserted on the posted op. Real CDP input throughout.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	recordClips, withPage, open, deliver, posted, reset, center, key, clickAt, ldOps, rect, wheel, sleep, byId,
	LD, LD_LONG
} from './diagram-helpers.mjs';

recordClips('ld-keys');

const N = (b) => key(b, 'n', 'KeyN', 78);
const M = (b) => key(b, 'm', 'KeyM', 77);
const withRung = (model, i, patch) => ({
	...model,
	rungs: model.rungs.map((r, j) => (j === i ? { ...r, ...patch(r) } : r))
});

test('Ladder keys: N on a selected contact posts toggleNeg, again flips it back (L09)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await clickAt(b, await center(b, byId('contact', 'a')));
		await reset(b);
		await N(b);
		let ops = await ldOps(b);
		assert.deepEqual(ops, [{ type: 'toggleNeg', rung: 'r1', path: [0] }]);
		// The host rewrites the file and re-sends the model: now NC.
		const nc = withRung(LD, 0, (r) => ({ elements: [{ ...r.elements[0], neg: true }, r.elements[1]] }));
		await deliver(b, { type: 'ldModel', model: nc, title: 'p.ld' });
		await reset(b);
		await N(b);
		ops = await ldOps(b);
		assert.deepEqual(ops, [{ type: 'toggleNeg', rung: 'r1', path: [0] }], 'the selection survives the refresh');
		// A coil is not a contact: N does nothing.
		await clickAt(b, await center(b, byId('coil', 'y')));
		await reset(b);
		await N(b);
		assert.deepEqual(await ldOps(b), []);
	});
});

test('Ladder keys: M on a selected coil cycles normal -> set -> reset -> normal (L10)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await clickAt(b, await center(b, byId('coil', 'y')));
		const modes = [];
		let mode;
		for (let i = 0; i < 3; i++) {
			await reset(b);
			await M(b);
			const ops = await ldOps(b);
			assert.equal(ops.length, 1, JSON.stringify(ops));
			assert.deepEqual({ type: ops[0].type, rung: ops[0].rung, coil: ops[0].coil }, { type: 'setCoilMode', rung: 'r1', coil: 0 });
			modes.push(ops[0].mode);
			mode = ops[0].mode;
			const next = withRung(LD, 0, (r) => ({ coils: [{ ...r.coils[0], mode }] }));
			await deliver(b, { type: 'ldModel', model: next, title: 'p.ld' });
		}
		assert.deepEqual(modes, ['S', 'R', '']);
		// A contact is not a coil: M does nothing.
		await clickAt(b, await center(b, byId('contact', 'a')));
		await reset(b);
		await M(b);
		assert.deepEqual(await ldOps(b), []);
	});
});

const scrollPos = (b) => b.eval(`(() => { const s = document.querySelector('.zpane .flow'); return { left: s.scrollLeft, top: s.scrollTop, max: s.scrollHeight - s.clientHeight }; })()`);

test('Ladder pan: a middle-button drag scrolls the pane by the drag delta and posts no op; the wheel scrolls too (L17)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD_LONG, title: 'p.ld' });
		const pane = await rect(b, '.zpane .flow');
		const start = await scrollPos(b);
		assert.ok(start.max > 150, 'fixture must overflow vertically: ' + JSON.stringify(start));
		await reset(b);
		const x = pane.x + pane.w / 2;
		const y = pane.y + pane.h / 2;
		const mid = (type, py, buttons) =>
			b.send('Input.dispatchMouseEvent', { type, x, y: py, button: type === 'mouseMoved' ? 'none' : 'middle', buttons, clickCount: 1 });
		await b.moveTo(x, y);
		await mid('mousePressed', y, 4);
		for (const d of [-20, -60, -100]) {
			await mid('mouseMoved', y + d, 4);
			await sleep(30);
		}
		await mid('mouseReleased', y - 100, 0);
		await sleep(150);
		const after = await scrollPos(b);
		// Dragging the content up by 100 px scrolls the pane down by 100.
		assert.ok(Math.abs(after.top - start.top - 100) <= 2, JSON.stringify({ start, after }));
		assert.deepEqual(await ldOps(b), [], 'a pan is not an edit');
		assert.deepEqual((await posted(b)).filter((m) => m.type !== 'ready'), [], 'and posts nothing');
		// Moving after release must not keep panning.
		await b.mouseMove(x, y + 50, { buttons: 0 });
		await sleep(100);
		assert.equal((await scrollPos(b)).top, after.top);
		// Plain wheel scrolls the pane.
		await wheel(b, x, y, 80, 0);
		await sleep(250);
		const wheeled = await scrollPos(b);
		assert.ok(wheeled.top > after.top, JSON.stringify({ after, wheeled }));
		assert.deepEqual(await ldOps(b), []);
	});
});

test('Ladder redo: Ctrl+Z / Ctrl+Shift+Z / Ctrl+Y post diagramKey undo / redo / redo (L19)', async () => {
	await withPage(
		async (b) => {
			await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
			await clickAt(b, await center(b, byId('contact', 'a')));
			await N(b); // an edit to undo
			await reset(b);
			await key(b, 'z', 'KeyZ', 90, 2);
			await key(b, 'z', 'KeyZ', 90, 2 | 8);
			await key(b, 'y', 'KeyY', 89, 2);
			const msgs = await posted(b);
			assert.deepEqual(msgs, [
				{ type: 'diagramKey', action: 'undo' },
				{ type: 'diagramKey', action: 'redo' },
				{ type: 'diagramKey', action: 'redo' }
			]);
		},
		{ forwardKeys: true }
	);
});

// Spot centres by rung / op / index.
const spots = (b) =>
	b.eval(`[...document.querySelectorAll('.spot')].map((el) => { const s = JSON.parse(el.getAttribute('data-spot')); const r = el.getBoundingClientRect(); return { rung: s.rung, op: s.spot.op, index: s.spot.index, series: s.spot.series, x: r.left + r.width / 2, y: r.top + r.height / 2 }; })`);

async function dragTo(b, from, to) {
	await b.mouseDown(from.x, from.y);
	await b.mouseMove(from.x + 12, from.y + 12);
	await b.mouseMove((from.x + to.x) / 2, (from.y + to.y) / 2);
	await b.mouseMove(to.x, to.y);
	await b.mouseUp(to.x, to.y);
	await sleep(150);
}

test('Ladder drag: a contact dragged into another rung posts ONE move naming source path and target (L07)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		const all = await spots(b);
		const target = all.find((s) => s.rung === 'r2' && s.op === 'insert' && s.index === 0);
		assert.ok(target, JSON.stringify(all));
		const a = await center(b, byId('contact', 'a'));
		await reset(b);
		await dragTo(b, a, target);
		const ops = await ldOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual(ops[0], { type: 'move', rung: 'r1', path: [0], toRung: 'r2', toPath: target.series ?? [], toIndex: 0 });
	});
});

test('Ladder drag: a contact dragged to a later spot in its own rung posts a move with the new index (L07)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		const all = await spots(b);
		const r1 = all.filter((s) => s.rung === 'r1' && s.op === 'insert');
		const later = r1.reduce((m, s) => (s.index > m.index ? s : m));
		assert.ok(later.index >= 2, 'a spot past the FB: ' + JSON.stringify(r1));
		const a = await center(b, byId('contact', 'a'));
		await reset(b);
		await dragTo(b, a, later);
		const ops = await ldOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual(ops[0], { type: 'move', rung: 'r1', path: [0], toRung: 'r1', toPath: later.series ?? [], toIndex: later.index });
	});
});

test('Ladder drag: a cross-rung drag leaves no text selected, mid-drag or after the drop (#131)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		const all = await spots(b);
		const target = all.find((s) => s.rung === 'r2' && s.op === 'insert' && s.index === 0);
		// Grab the contact by its operand label: text under the press is what lets
		// the browser start a selection.
		const lab = await rect(b, '.operand', 0);
		const a = { x: lab.cx, y: lab.cy };
		await reset(b);
		await b.mouseDown(a.x, a.y);
		await b.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: a.x + 12, y: a.y + 12, button: 'left', buttons: 1 });
		// Real drags report button 'left' on every move, which is what lets the
		// browser extend a text selection.
		// Sweep across the other rung's labels on the way to the drop.
		const labels = await b.eval(`[...document.querySelectorAll('.rsvg text')].map((el) => { const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; }).filter((p) => p.y > ${a.y})`);
		for (const p of [...labels, target]) await b.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: p.x, y: p.y, button: 'left', buttons: 1 });
		const mid = await b.eval(`document.getSelection().toString()`);
		await b.mouseUp(target.x, target.y);
		await sleep(150);
		const after = await b.eval(`document.getSelection().toString()`);
		assert.equal(mid, '', 'nothing selected mid-drag');
		assert.equal(after, '', 'nothing selected after the drop');
		assert.equal((await ldOps(b)).length, 1, 'the move itself still posts');
	});
});
