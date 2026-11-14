// Pipes that cross without joining, in the mimic editor: the flatter one
// (a header) passes over and the other is masked out either side of it, as
// the kit's Mimic draws it on the screen; a pipe ending on a header is a tee
// and gets a dot instead.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, withEditor, sleepMs, exceptions } from './mimic-helpers.mjs';

recordClips('mimic-crossings');

const doc = () => ({
	name: 'crossings',
	canvas: { width: 600, height: 400 },
	equipment: [],
	pipes: [
		{ id: 'H1', points: [[40, 150], [560, 150]] },
		{ id: 'H2', points: [[40, 300], [560, 300]] },
		{ id: 'D1', points: [[200, 60], [200, 300]] }, // crosses H1, ends on H2
		{ id: 'D2', points: [[400, 60], [400, 150]] } // ends on H1
	],
	labels: []
});

test('X01 a drop crossing a header is broken under it; a tee is dotted', async () => {
	await withEditor(doc(), async (ed) => {
		await sleepMs(300);
		// D1 (the third run) is masked round 200,150, where it passes under H1
		const masks = await ed.b.eval(
			`JSON.stringify([...document.querySelectorAll('g.piperun[mask]')].map((g) => g.dataset.id))`
		);
		assert.deepEqual([...new Set(JSON.parse(masks))], ['D1'], 'only D1 passes under a header');
		const cut = await ed.b.eval(
			`(() => { const g = document.querySelector('g.piperun[mask]'); const id = g.getAttribute('mask').slice(5, -1); const r = document.getElementById(id).querySelectorAll('rect')[1]; return [r.getAttribute('x'), r.getAttribute('y'), r.getAttribute('width')].join(','); })()`
		);
		assert.equal(cut, '189,139,22');
		// D1 ends on H2 and D2 on H1: two tees, each dotted on its header
		const tees = await ed.b.eval(
			`JSON.stringify([...document.querySelectorAll('circle.tee')].map((c) => [+c.getAttribute('cx'), +c.getAttribute('cy')]).sort((a, b) => a[0] - b[0]))`
		);
		assert.deepEqual(JSON.parse(tees), [[200, 300], [400, 150]]);
		assert.deepEqual(exceptions(ed), []);
	});
});
