// FBD parity gestures (the tia-shaped build's findings, #204–#209): the
// EN/ENO pin gesture (F19, X48), numbered networks (F20, X49), the
// palette's project FUNCTIONs (#204), the FB picker leaving defaulted
// inputs unbound (#205), and a blank file's seed name (#209). Real CDP
// input against the production bundle; asserts on the DOM and on the ops
// the webview posts. See INVENTORY.md.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	recordClips, sleep, withPage, deliver, reset, center, node, fbdOps, rect, clickAt, key, btnByText, typeText, text
} from './diagram-helpers.mjs';

recordClips('fbd-parity');

// `naut fbd graph` of:
//   NETWORK 'Copy A' / B := A
//   NETWORK 'Count A' / // counts rising edges of A / c1 : CTU(…) / Cnt := c1.CV
//   NETWORK / C := c1.Q
const NETS = {"name":"Main","nodes":[{"id":"c:B","kind":"coil","label":"B","layer":2,"line":5,"net":1,"exec":1},{"id":"c:Cnt","kind":"coil","label":"Cnt","layer":2,"line":9,"net":2,"exec":2},{"id":"c:C","kind":"coil","label":"C","layer":2,"line":11,"net":3,"exec":1},{"id":"f:c1","kind":"fb","label":"c1","type":"CTU","inputs":["CU","R","PV"],"outputs":["Q","CV"],"layer":1,"line":8,"net":2,"exec":1},{"id":"v:A","kind":"input","label":"A","layer":1,"line":5,"net":1},{"id":"k:0","kind":"input","label":"FALSE","layer":0,"src":{"line":8,"col":26,"endLine":8,"endCol":31,"text":"FALSE"},"line":8,"net":2},{"id":"k:1","kind":"input","label":"10","layer":0,"src":{"line":8,"col":39,"endLine":8,"endCol":41,"text":"10"},"line":8,"net":2},{"id":"v:c1.Q","kind":"input","label":"c1.Q","layer":1,"line":11,"net":3},{"id":"v:A#2","kind":"input","label":"A","layer":0,"line":5,"net":2},{"id":"cm:0","kind":"comment","label":"counts rising edges of A","layer":0,"line":7,"net":2}],"edges":[{"from":"v:A","to":"c:B","arg":{"line":5,"col":8,"endLine":5,"endCol":9,"text":"A"}},{"from":"v:A#2","to":"f:c1","toPin":"CU","arg":{"line":8,"col":18,"endLine":8,"endCol":19,"text":"A"}},{"from":"k:0","to":"f:c1","toPin":"R","arg":{"line":8,"col":26,"endLine":8,"endCol":31,"text":"FALSE"}},{"from":"k:1","to":"f:c1","toPin":"PV","arg":{"line":8,"col":39,"endLine":8,"endCol":41,"text":"10"}},{"from":"f:c1","fromPin":"CV","to":"c:Cnt","arg":{"line":9,"col":10,"endLine":9,"endCol":15,"text":"c1.CV"}},{"from":"v:c1.Q","to":"c:C","arg":{"line":11,"col":8,"endLine":11,"endCol":12,"text":"c1.Q"}}],"vars":[{"name":"A","type":"BOOL","section":"VAR_EXTERNAL","line":2},{"name":"B","type":"BOOL","section":"VAR_EXTERNAL","line":2},{"name":"C","type":"BOOL","section":"VAR_EXTERNAL","line":2},{"name":"Cnt","type":"DINT","section":"VAR_EXTERNAL","line":2}],"networks":[{"number":1,"title":"Copy A","line":4},{"number":2,"title":"Count A","line":6},{"number":3,"line":10}]};

// `naut fbd graph` of: sp = LIMIT(0.0, X, 10.0) / Out := sp /
//   t1 : TON(EN := En, IN := En, PT := T#1S) / Lamp := t1.Q / Ok := Lamp
const ENO = {"name":"Main","nodes":[{"id":"c:Out","kind":"coil","label":"Out","layer":2,"line":5,"exec":1},{"id":"c:Lamp","kind":"coil","label":"Lamp","layer":2,"line":7,"exec":3},{"id":"c:Ok","kind":"coil","label":"Ok","layer":2,"line":8,"exec":4},{"id":"f:t1","kind":"fb","label":"t1","type":"TON","inputs":["EN","IN","PT"],"outputs":["Q","ET"],"layer":1,"line":6,"exec":2},{"id":"b:w.sp","kind":"block","label":"LIMIT","wire":"sp","inputs":["MN","IN","MX"],"outputs":["OUT"],"layer":1,"line":4},{"id":"k:0","kind":"input","label":"0.0","layer":0,"src":{"line":4,"col":14,"endLine":4,"endCol":17,"text":"0.0"},"line":4},{"id":"v:X","kind":"input","label":"X","layer":0,"line":4},{"id":"k:1","kind":"input","label":"10.0","layer":0,"src":{"line":4,"col":22,"endLine":4,"endCol":26,"text":"10.0"},"line":4},{"id":"v:En","kind":"input","label":"En","layer":0,"line":6},{"id":"k:2","kind":"input","label":"T#1S","layer":0,"src":{"line":6,"col":38,"endLine":6,"endCol":42,"text":"T#1S"},"line":6},{"id":"v:Lamp","kind":"input","label":"Lamp","layer":1}],"edges":[{"from":"k:0","to":"b:w.sp","toPin":"MN","arg":{"line":4,"col":14,"endLine":4,"endCol":17,"text":"0.0"}},{"from":"v:X","to":"b:w.sp","toPin":"IN","arg":{"line":4,"col":19,"endLine":4,"endCol":20,"text":"X"}},{"from":"k:1","to":"b:w.sp","toPin":"MX","arg":{"line":4,"col":22,"endLine":4,"endCol":26,"text":"10.0"}},{"from":"b:w.sp","fromPin":"OUT","to":"c:Out","wire":"sp","arg":{"line":5,"col":10,"endLine":5,"endCol":12,"text":"sp"}},{"from":"v:En","to":"f:t1","toPin":"EN","arg":{"line":6,"col":18,"endLine":6,"endCol":20,"text":"En"}},{"from":"v:En","to":"f:t1","toPin":"IN","arg":{"line":6,"col":28,"endLine":6,"endCol":30,"text":"En"}},{"from":"k:2","to":"f:t1","toPin":"PT","arg":{"line":6,"col":38,"endLine":6,"endCol":42,"text":"T#1S"}},{"from":"f:t1","fromPin":"Q","to":"c:Lamp","arg":{"line":7,"col":11,"endLine":7,"endCol":15,"text":"t1.Q"}},{"from":"v:Lamp","to":"c:Ok","arg":{"line":8,"col":9,"endLine":8,"endCol":13,"text":"Lamp"}}],"vars":[{"name":"En","type":"BOOL","section":"VAR_EXTERNAL","line":2},{"name":"X","type":"REAL","section":"VAR_EXTERNAL","line":2},{"name":"Out","type":"REAL","section":"VAR_EXTERNAL","line":2},{"name":"Ok","type":"BOOL","section":"VAR_EXTERNAL","line":2},{"name":"Lamp","type":"BOOL","section":"VAR_EXTERNAL","line":2}]};

/** Real drag with intermediate moves. */
async function drag(b, from, to, steps = 8) {
	await b.moveTo(from.x, from.y);
	await b.send('Input.dispatchMouseEvent', { type: 'mousePressed', x: from.x, y: from.y, button: 'left', buttons: 1, clickCount: 1 });
	for (let i = 1; i <= steps; i++) {
		await b.send('Input.dispatchMouseEvent', {
			type: 'mouseMoved',
			x: from.x + ((to.x - from.x) * i) / steps,
			y: from.y + ((to.y - from.y) * i) / steps,
			button: 'left',
			buttons: 1
		});
		await sleep(25);
	}
	await b.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: to.x, y: to.y, button: 'left', buttons: 0, clickCount: 1 });
	await sleep(250);
}
const handle = (b, id, side, pin) => center(b, `${node(id)} .svelte-flow__handle.${side}[data-pin="${pin}"]`);
const handles = (b, id) =>
	b.eval(`[...document.querySelectorAll('${node(id)} .svelte-flow__handle')].map((h) => (h.classList.contains('target') ? 'in:' : 'out:') + h.dataset.pin + (h.classList.contains('open-pin') ? '(open)' : ''))`);
const band = (id) => `[data-kind="network"][data-id="${id}"]`;
const selectAllKey = (b) =>
	b.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', key: 'a', code: 'KeyA', windowsVirtualKeyCode: 65, modifiers: 2 });

test('FBD EN/ENO: bound EN draws without asking; the EN toggle opens EN/ENO pins to wire (F19, #206)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: ENO, title: 'main.fbd' });
		// A bound EN is a pin like any other, drawn first.
		assert.deepEqual(await handles(b, 'f:t1'), ['in:EN', 'in:IN', 'in:PT', 'out:Q', 'out:ET']);
		// LIMIT binds neither: no EN/ENO until the gesture.
		assert.deepEqual(await handles(b, 'b:w.sp'), ['in:MN', 'in:IN', 'in:MX', 'out:OUT']);
		await clickAt(b, await center(b, `${node('b:w.sp')} .eno-toggle`));
		await sleep(200);
		assert.deepEqual(await handles(b, 'b:w.sp'), ['in:EN(open)', 'in:MN', 'in:IN', 'in:MX', 'out:OUT', 'out:ENO(open)']);
		assert.deepEqual(await fbdOps(b), [], 'showing the pins writes nothing');
		// Drop a tag on EN: the text gains EN := En.
		await reset(b);
		await drag(b, await handle(b, 'v:En', 'source', ''), await handle(b, 'b:w.sp', 'target', 'EN'));
		assert.deepEqual(await fbdOps(b), [{ type: 'rewire', to: 'b:w.sp', toPin: 'EN', source: 'v:En', sourcePin: '' }]);
		// Drag from ENO onto a coil: the coil reads sp.ENO.
		await reset(b);
		await drag(b, await handle(b, 'b:w.sp', 'source', 'ENO'), await handle(b, 'c:Ok', 'target', ''));
		assert.deepEqual(await fbdOps(b), [{ type: 'rewire', to: 'c:Ok', toPin: '', source: 'b:w.sp', sourcePin: 'ENO' }]);
		// The toggle again hides the open pins (a bound EN would stay).
		await clickAt(b, await center(b, `${node('b:w.sp')} .eno-toggle`));
		await sleep(200);
		assert.deepEqual(await handles(b, 'b:w.sp'), ['in:MN', 'in:IN', 'in:MX', 'out:OUT']);
		// The FB has the toggle too; its bound EN is not "open".
		await clickAt(b, await center(b, `${node('f:t1')} .eno-toggle`));
		await sleep(200);
		assert.deepEqual(await handles(b, 'f:t1'), ['in:EN', 'in:IN', 'in:PT', 'out:Q', 'out:ET', 'out:ENO(open)']);
	});
});

test('FBD networks: numbered bands in order, statements with their execution order (F20, X49, #207)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: NETS, title: 'main.fbd' });
		const heads = await b.eval(`[...document.querySelectorAll('[data-kind="network"] .head')].map((h) => h.querySelector('.netno').textContent + ' | ' + h.querySelector('.ntitle').textContent)`);
		assert.deepEqual(heads, ['Network 1 | Copy A', 'Network 2 | Count A', 'Network 3 | add a title']);
		// Bands stack top to bottom; each statement sits inside its own band.
		const r = {};
		for (const id of ['n:1', 'n:2', 'n:3']) r[id] = await rect(b, band(id));
		assert.ok(r['n:1'].y + r['n:1'].h <= r['n:2'].y && r['n:2'].y + r['n:2'].h <= r['n:3'].y, 'bands in network order');
		const inside = async (id, n) => {
			const a = await rect(b, node(id));
			const f = r[n];
			return a.x >= f.x && a.y >= f.y && a.x + a.w <= f.x + f.w && a.y + a.h <= f.y + f.h;
		};
		assert.ok(await inside('c:B', 'n:1'), 'B in network 1');
		assert.ok(await inside('f:c1', 'n:2') && await inside('c:Cnt', 'n:2'), 'c1, Cnt in network 2');
		assert.ok(await inside('cm:0', 'n:2'), "network 2's comment stays with it");
		assert.ok(await inside('c:C', 'n:3'), 'C in network 3');
		// Execution order within a network: the call, then the coil reading it.
		const exec = (id) => b.eval(`document.querySelector('${node(id)} .exec')?.textContent ?? ''`);
		assert.deepEqual([await exec('f:c1'), await exec('c:Cnt'), await exec('c:B'), await exec('c:C')], ['1', '2', '1', '1']);
		// The read of network 2's c1.Q from network 3 is a box, not a wire across.
		assert.ok(await inside('v:c1.Q', 'n:3'));
		// The frame never eats canvas clicks: a point inside the band, off
		// every node, is the pane.
		const f = r['n:1'];
		assert.equal(await b.eval(`document.elementFromPoint(${f.x + f.w - 6}, ${f.y + f.h - 6})?.classList.contains('svelte-flow__pane')`), true);
	});
});

test('FBD networks: the header picks the palette target; buttons move, add, remove; double-click renames (F20)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: NETS, title: 'main.fbd' });
		await reset(b);
		await clickAt(b, await center(b, `${band('n:2')} .netno`));
		assert.equal(await b.eval(`document.querySelector('${band('n:2')}').classList.contains('active')`), true);
		await clickAt(b, await btnByText(b, '.bar button', '+ add'));
		assert.equal(await b.eval(`document.querySelector('[data-kind="net-target"]')?.textContent.trim()`), 'inserting into network 2');
		await clickAt(b, await btnByText(b, 'button.item', 'comment'));
		await reset(b);
		await clickAt(b, await btnByText(b, '.actions button', 'insert'));
		assert.deepEqual(await fbdOps(b), [{ type: 'insertStatement', text: '// note', node: 'n:2' }]);
		// The palette's "network" template adds one after the picked network.
		await clickAt(b, await btnByText(b, '.bar button', '+ add'));
		await clickAt(b, await btnByText(b, 'button.item', 'network'));
		await clickAt(b, await b.eval(`(() => { const r = document.querySelector('label.field input').getBoundingClientRect(); return { x: r.left + 10, y: r.top + r.height / 2 }; })()`));
		await typeText(b, 'Dose');
		await reset(b);
		await clickAt(b, await btnByText(b, '.actions button', 'insert'));
		assert.deepEqual(await fbdOps(b), [{ type: 'addNetwork', text: 'Dose', node: 'n:2' }]);
		// Header buttons.
		const btn = (id, act) => center(b, `${band(id)} .nbtn[data-act="${act}"]`);
		assert.equal(await b.eval(`document.querySelector('${band('n:1')} .nbtn[data-act="up"]').disabled`), true, 'the first cannot move up');
		assert.equal(await b.eval(`document.querySelector('${band('n:3')} .nbtn[data-act="down"]').disabled`), true, 'the last cannot move down');
		await reset(b);
		await clickAt(b, await btn('n:2', 'up'));
		await clickAt(b, await btn('n:1', 'add'));
		await clickAt(b, await btn('n:3', 'remove'));
		assert.deepEqual(await fbdOps(b), [
			{ type: 'moveNetwork', node: 'n:2', value: 'up' },
			{ type: 'addNetwork', node: 'n:1', text: '' },
			{ type: 'removeNetwork', node: 'n:3' }
		]);
		// Double-click the (empty) title: the float editor renames.
		const t = await center(b, `${band('n:3')} .ntitle`);
		await b.dblclick(t.x, t.y);
		await sleep(200);
		assert.match(await b.eval(`document.activeElement?.tagName`), /INPUT|TEXTAREA/);
		await selectAllKey(b);
		await typeText(b, 'Lamp');
		await reset(b);
		await key(b, 'Enter', 'Enter', 13);
		assert.deepEqual(await fbdOps(b), [{ type: 'renameNetwork', node: 'n:3', text: 'Lamp' }]);
	});
});

test("FBD palette: the function field offers the project's FUNCTIONs by their declared names (#204)", async () => {
	await withPage(async (b) => {
		const model = {
			...ENO,
			funcs: [{ name: 'ScaleAnalog', user: true, prefix: 's', result: 'REAL', detail: '(Raw, EngLo, EngHi) → REAL', pins: [] }]
		};
		await deliver(b, { type: 'model', model, title: 'main.fbd' });
		await clickAt(b, await btnByText(b, '.bar button', '+ add'));
		await clickAt(b, await btnByText(b, 'button.item', 'block → wire'));
		const field = `[...document.querySelectorAll('label.field')].find((l) => l.querySelector('span')?.textContent.trim() === 'function')?.querySelector('input')`;
		await clickAt(b, await b.eval(`(() => { const r = (${field}).getBoundingClientRect(); return { x: r.left + 10, y: r.top + r.height / 2 }; })()`));
		await selectAllKey(b);
		await typeText(b, 'Sca');
		const items = await b.eval(`[...document.querySelectorAll('.suggest .list button, .suggest .list .item')].map((x) => x.textContent.trim())`);
		assert.ok(items.some((t) => t.startsWith('ScaleAnalog')), `suggestions: ${items}`);
		assert.ok(items.some((t) => t.includes('(Raw, EngLo, EngHi) → REAL')), 'with its inputs and return type');
		await key(b, 'Tab', 'Tab', 9); // accept the first suggestion
		assert.equal(await b.eval(`(${field}).value`), 'ScaleAnalog');
		await reset(b);
		await clickAt(b, await btnByText(b, '.actions button', 'insert'));
		assert.deepEqual(await fbdOps(b), [{ type: 'insertStatement', text: 'w1 = ScaleAnalog(in1, in2)' }]);
	});
});

test('FBD palette: the FB picker leaves an input with a declared default unbound (#205)', async () => {
	await withPage(async (b) => {
		// An older CLI's catalog (no args): the webview applies the same rule.
		const model = {
			...ENO,
			fbTypes: [
				{
					name: 'Dosing', user: true, prefix: 'd',
					pins: [
						{ name: 'Start', type: 'BOOL', dir: 'in' },
						{ name: 'NoFlowTime', type: 'TIME', dir: 'in', init: 'T#5S' },
						{ name: 'Recipe', type: 'DoseRecipe', dir: 'inout' },
						{ name: 'Done', type: 'BOOL', dir: 'out' }
					]
				}
			]
		};
		await deliver(b, { type: 'model', model, title: 'main.fbd' });
		await clickAt(b, await btnByText(b, '.bar button', '+ add'));
		await clickAt(b, await btnByText(b, 'button.item', 'function block'));
		await typeText(b, 'Dosing');
		await key(b, 'Enter', 'Enter', 13);
		assert.equal(await b.eval(`document.querySelector('.fbpick input.fbargs').value`), 'Start := _, Recipe := _');
		await reset(b);
		await key(b, 'Enter', 'Enter', 13);
		assert.deepEqual(await fbdOps(b), [{ type: 'insertStatement', text: 'd1 : Dosing(Start := _, Recipe := _)' }]);
	});
});

test('FBD: a blank main.fbd seeds PROGRAM Main, the name naut new would give it (#209)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: { name: '', nodes: [], edges: [], blank: true }, title: 'main.fbd' });
		assert.match(await text(b), /PROGRAM Main skeleton/);
		await reset(b);
		await clickAt(b, await center(b, '.blank button'));
		assert.deepEqual(await fbdOps(b), [{ type: 'init', pou: 'Main' }]);
	});
});
