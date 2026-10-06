// The force table in the diagrams (INVENTORY X43): the extension posts the
// frame's `forces` block (lowercased address → value) alongside the live
// values, and every live overlay marks a forced value with an F — the FBD
// chip and the VARS panel pill (.nx-pill.forced, the badge drawn by
// theme.css's ::before), a ladder operand's live value, an SFC action
// target. The SFC chart also tags each step and transition with
// data-vscode-context, which is what VS Code's webview/context menu keys
// "Set Active Step" / "Fire Transition" on.
//
// Run: `npm run test:gestures` (from webview-ui, after `npm run build`).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, withPage, deliver, FBD, LD, SFC, sleep } from './diagram-helpers.mjs';

recordClips('force');

const live = (values, forced) => ({ type: 'liveValues', enabled: true, fresh: true, values, forced });

// Every pill with its text and whether it carries the F badge.
const pills = (b) =>
	b.eval(`[...document.querySelectorAll('.nx-pill')].map((el) => ({
		text: el.textContent.trim(),
		forced: el.classList.contains('forced'),
		badge: getComputedStyle(el, '::before').content
	}))`);

test('FBD: a forced input chip and its VARS pill carry the F badge; unforced ones do not', async () => {
	await withPage(async (b) => {
		const model = {
			...FBD,
			vars: [
				{ name: 'A', type: 'BOOL', section: 'VAR_EXTERNAL', line: 3 },
				{ name: 'B', type: 'BOOL', section: 'VAR_EXTERNAL', line: 3 }
			]
		};
		await deliver(b, { type: 'model', model, title: 'n.fbd' });
		await deliver(b, live({ a: true, b: false, y: false }, { a: true }));
		await sleep(200);
		const ps = await pills(b);
		const forced = ps.filter((p) => p.forced);
		assert.ok(forced.length >= 1, `no forced pill among ${JSON.stringify(ps)}`);
		for (const p of forced) {
			assert.equal(p.text, 'TRUE', 'a forced pill still shows the value');
			assert.equal(p.badge, '"F"', 'the F badge is drawn');
		}
		assert.ok(ps.some((p) => !p.forced && p.text === 'FALSE'), 'B stays an ordinary pill');
		for (const p of ps.filter((p) => !p.forced)) assert.equal(p.badge, 'none', 'no badge on an unforced pill');

		// The force removed: the next frame carries no table, the badge goes.
		await deliver(b, live({ a: false, b: false, y: false }, {}));
		await sleep(200);
		assert.deepEqual((await pills(b)).filter((p) => p.forced), [], 'badge gone once unforced');
	});
});

test('Ladder: a forced operand shows F ahead of its live value', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await deliver(b, live({ a: true, b: true, y: true, z: true, t1: { Q: false, ET: 0 } }, { b: true }));
		await sleep(200);
		const vals = await b.eval(`[...document.querySelectorAll('text.liveval')].map((t) => ({
			text: t.textContent, mark: !!t.querySelector('tspan.nx-forced-mark')
		}))`);
		const marked = vals.filter((v) => v.mark);
		assert.equal(marked.length, 1, `exactly the forced contact is marked: ${JSON.stringify(vals)}`);
		assert.equal(marked[0].text, 'F TRUE');
		assert.ok(vals.some((v) => !v.mark && v.text === 'TRUE'), 'the unforced operands are plain');
	});
});

test('SFC: steps and transitions carry the context the Set Active Step / Fire Transition menu keys on', async () => {
	await withPage(async (b) => {
		const model = {
			...SFC,
			steps: SFC.steps.map((s) =>
				s.name === 'Run' ? { ...s, actions: [{ qualifier: 'N', target: 'Pump', line: 6 }] } : s
			),
			trans: [...SFC.trans, { id: 'tr:11', name: 'Back', from: ['Run'], to: ['Idle'], cond: 'done', kind: 'normal', line: 11, endLine: 12 }]
		};
		await deliver(b, { type: 'sfcModel', model, title: 'seq.sfc' });
		const ctx = await b.eval(`[...document.querySelectorAll('[data-vscode-context]')].map((el) => JSON.parse(el.getAttribute('data-vscode-context')))`);
		const steps = ctx.filter((c) => c.nautilusSfcStep).map((c) => c.nautilusSfcStep).sort();
		const trans = ctx.filter((c) => c.nautilusSfcTransition).map((c) => c.nautilusSfcTransition).sort();
		assert.deepEqual(steps, ['Idle', 'Run', 'Spare']);
		// An unnamed transition is addressed the way the transpiler names it.
		assert.deepEqual(trans, ['Back', 't7']);

		// A forced action target is marked on the chart.
		await deliver(b, live({ _s_run_x: true, _s_idle_x: false, pump: true }, { pump: true }));
		await sleep(200);
		const marks = await b.eval(`[...document.querySelectorAll('.assoctarget')].map((t) => ({ text: t.textContent, mark: !!t.querySelector('.nx-forced-mark') }))`);
		assert.deepEqual(marks, [{ text: 'PumpF', mark: true }]);
		await deliver(b, live({ _s_run_x: true, _s_idle_x: false, pump: true }, {}));
		await sleep(200);
		const after = await b.eval(`[...document.querySelectorAll('.assoctarget .nx-forced-mark')].length`);
		assert.equal(after, 0);
	});
});
