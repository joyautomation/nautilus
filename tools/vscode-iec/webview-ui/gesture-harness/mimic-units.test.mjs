// A screen's units (a tank repeated, written once) in the mimic editor: each
// copy is drawn where the screen draws it, locked (it is edited in the
// file, in its unit), and a pipe in the unit runs to its copy's port.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, withEditor, sleepMs, exceptions } from './mimic-helpers.mjs';

recordClips('mimic-units');

const unitDoc = () => ({
	name: 'units',
	canvas: { width: 900, height: 500 },
	equipment: [{ id: 'T0', component: 'Tank', x: 40, y: 180, width: 100, label: 'T0' }],
	pipes: [],
	labels: [],
	units: [
		{
			id: 'tank',
			each: [1, 2, 3],
			step: [220, 0],
			equipment: [{ id: 'T{n}', component: 'Tank', x: 220, y: 180, width: 100, label: 'T-{n}' }],
			pipes: [{ id: 'P{n}', points: [[270, 60]], to: { equip: 'T{n}', port: 'top' } }],
			labels: [{ text: 'tank {n}', x: 230, y: 420 }]
		}
	]
});

const copies = (ed) =>
	ed.b
		.eval(
			`JSON.stringify([...document.querySelectorAll('.eq.unitcopy')].map((e) => { const r = e.getBoundingClientRect(); return { unit: e.dataset.unit, cx: r.left + r.width / 2, cy: r.top + r.height / 2 }; }))`
		)
		.then(JSON.parse);

test('U01 a unit is drawn once per value, locked, with its pipes and labels', async () => {
	await withEditor(unitDoc(), async (ed) => {
		await sleepMs(300);
		const c = await copies(ed);
		assert.deepEqual(c.map((x) => x.unit), ['tank#1', 'tank#2', 'tank#3']);
		// 220 canvas units apart, left to right
		assert.ok(c[0].cx < c[1].cx && c[1].cx < c[2].cx);
		// each copy's pipe draws, to its tank's top: a path with length
		const pipes = await ed.b.eval(
			`JSON.stringify([...document.querySelectorAll('.piperun.unitcopy path')].map((p) => p.getTotalLength()))`
		).then(JSON.parse);
		assert.ok(pipes.length >= 3 && pipes.filter((l) => l > 50).length >= 3, `pipe lengths ${pipes}`);
		const labels = await ed.b.eval(`JSON.stringify([...document.querySelectorAll('.lbl.unitcopy')].map((l) => l.textContent))`).then(JSON.parse);
		assert.deepEqual(labels, ['tank 1', 'tank 2', 'tank 3']);
		// locked: clicking a copy selects nothing and sends no op
		await ed.selectMode();
		await ed.clickCanvas(c[1].cx, c[1].cy);
		await sleepMs(80);
		assert.equal(await ed.b.eval("document.querySelectorAll('.eq.sel').length"), 0);
		assert.deepEqual(await ed.ops(), []);
		assert.deepEqual(exceptions(ed), []);
	});
});
