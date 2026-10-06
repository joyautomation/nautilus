// The SFC editor parity batch (#76, #179–#183, #186, #187): real input on the
// production bundle, assertions on the ops the chart posts and on what it
// draws. INVENTORY rows S23–S31.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, withPage, deliver, reset, center, key, esc, clickAt, sfcOps, paletteBtn, sleep, typeText, rect } from './diagram-helpers.mjs';

recordClips('sfc-parity');

const st = (name, line, extra = {}) => ({ id: 'st:' + name, name, initial: false, line, endLine: line + 1, ...extra });
const tr = (id, from, to, line, extra = {}) => ({ id, from, to, cond: 'c' + line, kind: 'normal', line, endLine: line + 1, ...extra });

// Idle -> Fill; Fill -> Aborted (declared first: priority 1) and Fill ->
// (Heat, Wash); Heat -> HeatDone; (Wash, HeatDone) -> Drain, the join with
// legs of different lengths; loop backs to Idle.
const WASHER = {
	name: 'Washer',
	vars: [{ name: 'FillValve', type: 'BOOL', section: 'VAR_EXTERNAL', line: 2 }],
	steps: [
		st('Idle', 4, { initial: true }),
		st('Fill', 6, { actions: [{ qualifier: 'S', target: 'DoorLock', line: 7 }, { qualifier: 'N', target: 'FillValve', line: 8 }] }),
		st('Heat', 10, { actions: [{ qualifier: 'N', target: 'HeatCtl', line: 11 }] }),
		st('Wash', 13),
		st('HeatDone', 15),
		st('Drain', 17),
		st('Aborted', 19)
	],
	trans: [
		tr('tr:30', ['Idle'], ['Fill'], 30, { name: 'T_start' }),
		tr('tr:32', ['Fill'], ['Aborted'], 32, { kind: 'alt' }),
		tr('tr:34', ['Fill'], ['Heat', 'Wash'], 34, { kind: 'simDiverge' }),
		tr('tr:36', ['Heat'], ['HeatDone'], 36),
		tr('tr:38', ['Wash', 'HeatDone'], ['Drain'], 38, { kind: 'simConverge' }),
		tr('tr:40', ['Drain'], ['Idle'], 40),
		tr('tr:42', ['Aborted'], ['Idle'], 42)
	],
	actions: [{ id: 'ac:DoorLock', name: 'DoorLock', body: 'Lock := TRUE;', line: 50, endLine: 52 }]
};

const show = (b, model = WASHER) => deliver(b, { type: 'sfcModel', model, title: 'washer.sfc' });
const sel = (b) => b.eval(`[...document.querySelectorAll('svg.chart .selected')].map((g) => g.dataset.id)`);
const boxOf = (b, id) => rect(b, `g.step[data-id="${id}"] rect.box`);
const assocAdd = (b, step) => center(b, `[data-kind="chip"][data-id="addassoc:st:${step}"] text`);
const floatInput = (b) => b.eval(`(() => { const el = document.activeElement; return el && (el.tagName + ':' + (el.value ?? '')); })()`);
const ctrlA = (b) => key(b, 'a', 'KeyA', 65, 2);
const enter = (b) => key(b, 'Enter', 'Enter', 13);
const arrow = (b, dir, modifiers = 0) => key(b, 'Arrow' + dir, 'Arrow' + dir, { Left: 37, Up: 38, Right: 39, Down: 40 }[dir], modifiers);
async function clickEmpty(b) {
	const svg = await rect(b, 'svg.chart');
	await clickAt(b, { x: svg.x + svg.w - 8, y: svg.y + svg.h - 8 });
}

test('S27 #186 SFC: "+ action" appends — the op carries the row count as its index', async () => {
	await withPage(async (b) => {
		await show(b);
		await reset(b);
		await clickAt(b, await assocAdd(b, 'Fill'));
		await ctrlA(b);
		await typeText(b, 'P1 CountCycle');
		await enter(b);
		assert.deepEqual(await sfcOps(b), [{ type: 'addAssoc', step: 'st:Fill', index: 2, qualifier: 'P1', target: 'CountCycle' }]);
	});
});

test('S27 #183 SFC: the action field takes "D Detergent T#3S"; text it cannot parse stays in the field with the reason', async () => {
	await withPage(async (b) => {
		await show(b);
		await reset(b);
		// the Codesys column order: qualifier, name, time
		await clickAt(b, await assocAdd(b, 'Heat'));
		await ctrlA(b);
		await typeText(b, 'D Detergent T#3S');
		await enter(b);
		assert.deepEqual(await sfcOps(b), [{ type: 'addAssoc', step: 'st:Heat', index: 1, qualifier: 'D', target: 'Detergent', time: 'T#3S' }]);
		// unparseable: nothing posted, the field reopens holding the text, the error shown
		await reset(b);
		await clickAt(b, await assocAdd(b, 'Heat'));
		await ctrlA(b);
		await typeText(b, 'Detergent for T#3S please');
		await enter(b);
		await sleep(150);
		assert.deepEqual(await sfcOps(b), [], 'nothing written');
		assert.equal(await floatInput(b), 'INPUT:Detergent for T#3S please', 'the typed text is still in the field');
		const alert = await b.eval(`document.querySelector('.ferr[role="alert"]')?.textContent ?? ''`);
		assert.match(alert, /D Detergent\(T#3S\)/, 'the error names the form');
		// fixing it in place commits
		await ctrlA(b);
		await typeText(b, 'D Detergent(T#3S)');
		await enter(b);
		assert.deepEqual(await sfcOps(b), [{ type: 'addAssoc', step: 'st:Heat', index: 1, qualifier: 'D', target: 'Detergent', time: 'T#3S' }]);
		assert.equal(await b.eval(`document.querySelectorAll('.ferr').length`), 0, 'the error goes with the field');
	});
});

test('S30 #187 SFC: a join whose legs differ in length is drawn as a convergence under both legs, not a ↩ jump', async () => {
	await withPage(async (b) => {
		await show(b);
		const join = await b.eval(`(() => { const g = document.querySelector('g.trans[data-id="tr:38"]'); return { jump: !!g.querySelector('g.jump'), bars: g.querySelectorAll('line.bar').length, legs: g.querySelectorAll('line.flowline').length }; })()`);
		assert.deepEqual(join, { jump: false, bars: 2, legs: 3 }, 'a double bar, two legs in and one out');
		const heatDone = await boxOf(b, 'st:HeatDone');
		const drain = await boxOf(b, 'st:Drain');
		assert.ok(drain.y > heatDone.y + heatDone.h, 'Drain sits below HeatDone');
		const bar = await rect(b, 'g.trans[data-id="tr:38"] rect.barhit');
		assert.ok(bar.cy > heatDone.y + heatDone.h && bar.cy < drain.y, 'the bar is between them');
	});
});

test('S23–S25 #76 SFC: arrow keys walk the chart along its flow; Enter edits the selection; transition names are drawn', async () => {
	await withPage(async (b) => {
		await show(b);
		await clickEmpty(b);
		await reset(b);
		await arrow(b, 'Down');
		assert.deepEqual(await sel(b), ['st:Idle'], 'nothing selected: the initial step');
		await arrow(b, 'Down');
		assert.deepEqual(await sel(b), ['tr:30']);
		await arrow(b, 'Down');
		assert.deepEqual(await sel(b), ['st:Fill']);
		await arrow(b, 'Down');
		assert.deepEqual(await sel(b), ['tr:32'], 'the highest-priority branch first');
		await arrow(b, 'Right');
		assert.deepEqual(await sel(b), ['tr:34'], 'the next alternative branch');
		await arrow(b, 'Down');
		assert.deepEqual(await sel(b), ['st:Heat']);
		await arrow(b, 'Right');
		assert.deepEqual(await sel(b), ['st:Wash'], 'the parallel step on the same row');
		await arrow(b, 'Up');
		assert.deepEqual(await sel(b), ['tr:34']);
		await arrow(b, 'Up');
		assert.deepEqual(await sel(b), ['st:Fill']);
		// → from a step with nothing to its right: into its actions, ↓ through them
		await arrow(b, 'Right');
		assert.deepEqual(await sel(b), ['st:Fill:0']);
		await arrow(b, 'Down');
		assert.deepEqual(await sel(b), ['st:Fill:1']);
		await arrow(b, 'Left');
		assert.deepEqual(await sel(b), ['st:Fill']);
		assert.deepEqual(await sfcOps(b), [], 'walking is not an edit');

		// Enter on a step renames it
		await enter(b);
		assert.equal(await floatInput(b), 'INPUT:Fill');
		await ctrlA(b);
		await typeText(b, 'Filling');
		await enter(b);
		assert.deepEqual(await sfcOps(b), [{ type: 'renameStep', step: 'st:Fill', newName: 'Filling' }]);
		// focus came back: the arrows still walk
		await arrow(b, 'Up');
		assert.deepEqual(await sel(b), ['tr:30']);
		// Enter on a transition edits its condition
		await reset(b);
		await enter(b);
		assert.equal(await floatInput(b), 'INPUT:c30');
		await esc(b);
		// the name is drawn above the condition
		assert.equal(await b.eval(`document.querySelector('g.trans[data-id="tr:30"] text.tname')?.textContent`), 'T_start');
		// F2 names it
		await key(b, 'F2', 'F2', 113);
		assert.equal(await floatInput(b), 'INPUT:T_start');
		await ctrlA(b);
		await typeText(b, 'T_go');
		await enter(b);
		assert.deepEqual(await sfcOps(b), [{ type: 'renameTransition', transition: 'tr:30', newName: 'T_go' }]);
		// Del still deletes the keyboard selection
		await reset(b);
		await key(b, 'Delete', 'Delete', 46);
		assert.deepEqual(await sfcOps(b), [{ type: 'deleteTransition', transition: 'tr:30' }]);
	});
});

test('S31 #76 SFC: the add forms take a transition name', async () => {
	await withPage(async (b) => {
		await show(b);
		await clickAt(b, await center(b, 'g.step[data-id="st:Drain"] rect.box'));
		await clickAt(b, await paletteBtn(b, '+ transition'));
		const labels = await b.eval(`[...document.querySelectorAll('.addform label > span:first-child')].map((s) => s.textContent.trim())`);
		assert.deepEqual(labels, ['to step', 'name', 'condition'], 'the condition stays the last field');
		// the focused "to step" picker: ↑ from "other…" to the last step
		await arrow(b, 'Up');
		await clickAt(b, await center(b, '.addform label:nth-of-type(2) input'));
		await typeText(b, 'T_empty');
		await reset(b);
		await enter(b);
		const [op] = await sfcOps(b);
		assert.equal(op.type, 'addTransition');
		assert.equal(op.name, 'T_empty');
		assert.deepEqual(op.from, ['Drain']);
assert.deepEqual(op.to, ['Aborted']);
		// "+ step" chained: the transition's name rides as transName
		await clickAt(b, await center(b, 'g.step[data-id="st:Drain"] rect.box'));
		await clickAt(b, await paletteBtn(b, '+ step'));
		await clickAt(b, await center(b, '.addform label:nth-of-type(3) input'));
		await typeText(b, 'T_spin');
		await reset(b);
		await enter(b);
		const [op2] = await sfcOps(b);
		assert.equal(op2.type, 'addStep');
		assert.equal(op2.transName, 'T_spin');
	});
});

test('S26 #181 SFC: ◀ priority / Alt+← move an alternative branch earlier; the bars carry their priority', async () => {
	await withPage(async (b) => {
		await show(b);
		const prios = await b.eval(`['tr:32', 'tr:34', 'tr:30'].map((id) => document.querySelector('g.trans[data-id="' + id + '"] g.prio text')?.textContent ?? null)`);
		assert.deepEqual(prios, ['1', '2', null], 'the two branches out of Fill are numbered; a lone transition is not');
		assert.equal(await paletteBtn(b, '◀ priority'), null, 'no reorder buttons without an alternative branch selected');
		await clickAt(b, await center(b, 'g.trans[data-id="tr:34"] rect.barhit'));
		assert.deepEqual(await sel(b), ['tr:34']);
		assert.equal(await b.eval(`[...document.querySelectorAll('.palette button')].find((x) => x.textContent.includes('priority ▶')).disabled`), true, 'already last');
		await reset(b);
		await clickAt(b, await paletteBtn(b, '◀ priority'));
		assert.deepEqual(await sfcOps(b), [{ type: 'moveTransition', transition: 'tr:34', delta: -1 }]);
		// the keyboard way, focus back on the chart
		await clickAt(b, await center(b, 'g.trans[data-id="tr:34"] rect.barhit'));
		await reset(b);
		await arrow(b, 'Left', 1);
		assert.deepEqual(await sfcOps(b), [{ type: 'moveTransition', transition: 'tr:34', delta: -1 }]);
		// the moved transition (an unnamed one changes id) stays selected once the model comes back
		const moved = { ...WASHER, trans: [WASHER.trans[0], { ...WASHER.trans[2], id: 'tr:32', line: 32 }, { ...WASHER.trans[1], id: 'tr:34', line: 34 }, ...WASHER.trans.slice(3)] };
		await show(b, moved);
		assert.deepEqual(await sel(b), ['tr:32'], 'the Fill -> (Heat, Wash) branch is still the selection');
		assert.equal(await b.eval(`document.querySelector('g.trans[data-id="tr:32"] g.prio text')?.textContent`), '1');
		await reset(b);
		await arrow(b, 'Left', 1);
		assert.deepEqual(await sfcOps(b), [], 'first already: nothing to do');
	});
});

test('S28 #182 SFC: double-click an association to an action that does not exist yet opens the ST-body editor; the qualifier edits the line', async () => {
	await withPage(async (b) => {
		await show(b);
		const row = (step, i) => `g.assocrow[data-id="st:${step}:${i}"]`;
		assert.equal(await b.eval(`document.querySelector('${row('Heat', 0)} text.assoctarget').classList.contains('isnew')`), true, 'HeatCtl is marked as a new action');
		await reset(b);
		const tgt = await center(b, `${row('Heat', 0)} text.assoctarget`);
		await b.dblclick(tgt.x, tgt.y);
		await sleep(200);
		assert.equal(await b.eval(`document.activeElement?.tagName`), 'TEXTAREA', 'the body editor');
		assert.match(await b.eval(`document.querySelector('.fcap')?.textContent ?? ''`), /new ACTION HeatCtl/);
		await typeText(b, 'Heater := TempC < TempSP;');
		await key(b, 'Enter', 'Enter', 13, 2);
		assert.deepEqual(await sfcOps(b), [{ type: 'setActionBody', action: 'HeatCtl', body: 'Heater := TempC < TempSP;' }]);
		// a declared variable's row opens the association line
		await reset(b);
		const v = await center(b, `${row('Fill', 1)} text.assoctarget`);
		await b.dblclick(v.x, v.y);
		await sleep(200);
		assert.equal(await floatInput(b), 'INPUT:N FillValve');
		await esc(b);
		// an existing ACTION's qualifier column edits the association line too
		const q = await center(b, `${row('Fill', 0)} text.assocq`);
		await b.dblclick(q.x, q.y);
		await sleep(200);
		assert.equal(await floatInput(b), 'INPUT:S DoorLock');
		await esc(b);
		// Enter on the keyboard-selected new-action row opens the body editor
		await clickAt(b, await center(b, `${row('Heat', 0)} text.assocq`));
		await enter(b);
		assert.equal(await b.eval(`document.activeElement?.tagName`), 'TEXTAREA');
		await esc(b);
		assert.deepEqual(await sfcOps(b), []);
	});
});

test('S28 #177/#210 SFC: an association naming a manifest tag is the tag, not a new action', async () => {
	await withPage(async (b) => {
		await show(b, { ...WASHER, tags: [{ name: 'HeatCtl', type: 'BOOL', role: 'output' }] });
		const row = `g.assocrow[data-id="st:Heat:0"]`;
		assert.equal(await b.eval(`document.querySelector('${row} text.assoctarget').classList.contains('isnew')`), false, 'HeatCtl is a tag');
		await reset(b);
		const tgt = await center(b, `${row} text.assoctarget`);
		await b.dblclick(tgt.x, tgt.y);
		await sleep(200);
		assert.equal(await floatInput(b), 'INPUT:N HeatCtl', 'the association line, not an ST-body editor');
		await esc(b);
	});
});

test('S29 #180 SFC: the vars panel declares a CONSTANT with its value; a refused declare keeps the typed name', async () => {
	await withPage(async (b) => {
		await show(b);
		const varsBtn = await b.eval(`(() => { const el = [...document.querySelectorAll('.bar button')].find((x) => x.textContent.trim() === 'vars'); const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
		await clickAt(b, varsBtn);
		const toggle = () => b.eval(`document.querySelector('.addrow button.toggle').textContent.trim()`);
		assert.equal(await toggle(), 'ext');
		await clickAt(b, await center(b, '.addrow button.toggle'));
		assert.equal(await toggle(), 'local');
		await clickAt(b, await center(b, '.addrow button.toggle'));
		assert.equal(await toggle(), 'const');
		await clickAt(b, await center(b, '.addrow input.grow'));
		await typeText(b, 'tMaxFill');
		await clickAt(b, await center(b, '.addrow .typefield input'));
		await ctrlA(b);
		await typeText(b, 'TIME');
		await esc(b);
		await clickAt(b, await center(b, '.addrow input.initfield'));
		await typeText(b, 'T#60S');
		await reset(b);
		await enter(b);
		assert.deepEqual(await sfcOps(b), [{ type: 'declareVar', name: 'tMaxFill', varType: 'TIME', section: 'VAR CONSTANT', init: 'T#60S' }]);
		// no model back yet (or the op was refused): the name is still there
		assert.equal(await b.eval(`document.querySelector('.addrow input.grow').value`), 'tMaxFill');
		// the declaration arrives: the row clears, the list shows it as a constant
		await show(b, { ...WASHER, vars: [...WASHER.vars, { name: 'tMaxFill', type: 'TIME', init: 'T#60S', section: 'VAR CONSTANT', line: 3 }] });
		assert.equal(await b.eval(`document.querySelector('.addrow input.grow').value`), '');
		const row = await b.eval(`(() => { const r = document.querySelector('.row[data-id="tMaxFill"]'); return r && r.querySelector('.badge').textContent.trim() + ' ' + r.querySelector('.type').textContent.trim(); })()`);
		assert.equal(row, 'const : TIME := T#60S');
	});
});
