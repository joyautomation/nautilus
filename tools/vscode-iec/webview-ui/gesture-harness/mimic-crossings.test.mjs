// Pipes that cross without joining, in the mimic editor: the flatter one
// (a header) passes over and the other is broken either side of it, as the
// kit's Mimic draws it on the screen; a pipe ending on a header is a tee and
// gets no break.
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

test('X01 a drop crossing a header is broken under it; a tee is not', async () => {
	await withEditor(doc(), async (ed) => {
		await sleepMs(300);
		const n = await ed.b.eval("document.querySelectorAll('g.crossing').length");
		assert.equal(n, 1, 'one crossing: D1 under H1');
		// the crossing redraws H1 (horizontal) over D1, clipped round 200,150
		const clip = await ed.b.eval(
			`(() => { const g = document.querySelector('g.crossing'); const id = g.getAttribute('clip-path').slice(5, -1); const r = document.getElementById(id).querySelector('rect'); return [r.getAttribute('x'), r.getAttribute('y')].join(','); })()`
		);
		assert.equal(clip, '191,141');
		const d = await ed.b.eval("document.querySelector('g.crossing path').getAttribute('d')");
		assert.match(d, /M 40 150 L 560 150/);
		assert.deepEqual(exceptions(ed), []);
	});
});
