// Ladder parity with the Studio 5000 habits the logix-shaped build probes
// (INVENTORY L21–L27): the edge contact drawn and live (#212), authored by
// palette, key and retag (#213); a FUNCTION_BLOCK's pins declared from the
// declare offer and the variables panel (#214); a whole rung copied and
// pasted (#217); the declare offer typing a counter's tag from its pin
// (#219); block instances in the variables panel, and Escape closing it
// (#220). Real CDP input throughout; the posted op is the assertion.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	recordClips, withPage, deliver, posted, reset, center, key, clickAt, ldOps, sleep, byId, esc, ctrlKey, typeText,
	paletteBtn, LD
} from './diagram-helpers.mjs';

recordClips('ld-parity');

const P = (b) => key(b, 'p', 'KeyP', 80);
const N = (b) => key(b, 'n', 'KeyN', 78);
const enter = (b) => key(b, 'Enter', 'Enter', 13);
const rungName = (name) => `svg.rsvg[data-id="${name}"] tspan.rungname`;
const withRung = (model, i, patch) => ({
	...model,
	rungs: model.rungs.map((r, j) => (j === i ? { ...r, ...patch(r) } : r))
});

// Rung m1 of the logix-shaped build: an ONS on the start button, a
// MotorStarter call, a coil.
const EDGE = {
	name: 'Conveyor',
	vars: [],
	rungs: [
		{
			name: 'm1',
			line: 5,
			endLine: 6,
			elements: [
				{ kind: 'edge', ref: 'M1_StartPB', mode: 'P', trig: 'rt_m1_M1_StartPB' },
				{ kind: 'fb', inst: 'm1', type: 'MotorStarter', args: 'Stop := M1_StopPB', powerIn: 'Start', powerOut: 'Run' }
			],
			coils: [{ kind: 'coil', ref: 'M1_Run' }]
		},
		{ name: 'trip', line: 7, endLine: 8, elements: [{ kind: 'edge', ref: 'Trip', mode: 'N', trig: 'ft_trip_Trip' }], coils: [{ kind: 'coil', ref: 'Alm' }] }
	]
};

// The stream's shape: lower-cased bases, an instance's outputs as members.
const live = (b, values) => deliver(b, { type: 'liveValues', enabled: true, fresh: true, values });
const nodeClass = (b, kind, id) => b.eval(`document.querySelector('${byId(kind, id)}')?.getAttribute('class') ?? null`);
// The stroke class of the wire leaving a node (its right-hand rung line).
const outWire = (b, kind, id) =>
	b.eval(`(() => { const g = document.querySelector('${byId(kind, id)}'); const ls = [...g.querySelectorAll('line.w')]; return ls[1]?.getAttribute('class') ?? null; })()`);

test('Ladder edge contact: +Tag / -Tag draw as P / N contacts between the rail and the block (L21, #212)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: EDGE, title: 'program.ld' });
		const g = await b.eval(`(() => { const g = document.querySelector('${byId('edge', 'M1_StartPB')}'); if (!g) return null; return { mark: g.querySelector('text.mark')?.textContent, operand: g.querySelector('text.operand')?.textContent, posts: g.querySelectorAll('line.post').length, x: g.getBoundingClientRect().left }; })()`);
		assert.ok(g, 'the edge contact is drawn');
		assert.deepEqual({ mark: g.mark, operand: g.operand, posts: g.posts }, { mark: 'P', operand: 'M1_StartPB', posts: 2 });
		const fbX = await b.eval(`document.querySelector('${byId('fb', 'm1')}').getBoundingClientRect().left`);
		assert.ok(g.x < fbX, 'the P contact sits ahead of the block, not the rail wired straight to it');
		assert.equal(await b.eval(`document.querySelector('${byId('edge', 'Trip')} text.mark')?.textContent`), 'N');
	});
});

test('Ladder edge contact: live power comes from its one-shot, and its input rules a pulse out (L21, #212)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: EDGE, title: 'program.ld' });
		await live(b, { m1_startpb: true, rt_m1_m1_startpb: { Q: true }, trip: true });
		assert.match(await nodeClass(b, 'edge', 'M1_StartPB'), /\bon\b/, 'the scan the one-shot fires');
		assert.match(await outWire(b, 'edge', 'M1_StartPB'), /\bon\b/, 'power flows on to the block');
		assert.match(await nodeClass(b, 'edge', 'Trip'), /\boff\b/, 'a falling edge cannot fire while its tag is TRUE');
		await live(b, { m1_startpb: true, rt_m1_m1_startpb: { Q: false }, trip: false });
		assert.match(await nodeClass(b, 'edge', 'M1_StartPB'), /\boff\b/, 'held: the one-shot is spent');
		assert.match(await outWire(b, 'edge', 'M1_StartPB'), /\boff\b/);
		const val = await b.eval(`document.querySelector('${byId('edge', 'M1_StartPB')} text.liveval')?.textContent`);
		assert.ok(val, 'the tag value shows under the contact, as on any contact');
	});
});

test('Ladder edge contact: the palette inserts P and N contacts (L22, #213)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await clickAt(b, await center(b, rungName('r2')));
		await reset(b);
		await clickAt(b, await paletteBtn(b, '⊣P⊢'));
		await clickAt(b, await paletteBtn(b, '⊣N⊢'));
		assert.deepEqual(await ldOps(b), [
			{ type: 'insert', rung: 'r2', kind: 'edge', mode: 'P', path: [], index: 999 },
			{ type: 'insert', rung: 'r2', kind: 'edge', mode: 'N', path: [], index: 999 }
		]);
	});
});

test('Ladder edge contact: P cycles a selected contact NO → P → N → NC → NO; N on an edge makes it NC (L23, #213)', async () => {
	await withPage(async (b) => {
		const forms = [
			{ kind: 'contact', ref: 'a' },
			{ kind: 'edge', ref: 'a', mode: 'P' },
			{ kind: 'edge', ref: 'a', mode: 'N' },
			{ kind: 'contact', ref: 'a', neg: true }
		];
		const seen = [];
		for (const el of forms) {
			const model = withRung(LD, 0, (r) => ({ elements: [el, r.elements[1]] }));
			await deliver(b, { type: 'ldModel', model, title: 'p.ld' });
			await clickAt(b, await center(b, byId(el.kind, 'a')));
			await reset(b);
			await P(b);
			const ops = await ldOps(b);
			assert.equal(ops.length, 1, JSON.stringify(ops));
			assert.deepEqual({ ...ops[0], mode: undefined }, { type: 'setContactForm', rung: 'r1', path: [0], mode: undefined });
			seen.push(ops[0].mode);
		}
		assert.deepEqual(seen, ['P', 'N', 'NC', 'NO']);
		// N on an edge: NC (toggleNeg is a contact's).
		await deliver(b, { type: 'ldModel', model: withRung(LD, 0, (r) => ({ elements: [forms[1], r.elements[1]] })), title: 'p.ld' });
		await clickAt(b, await center(b, byId('edge', 'a')));
		await reset(b);
		await N(b);
		assert.deepEqual(await ldOps(b), [{ type: 'setContactForm', rung: 'r1', path: [0], mode: 'NC' }]);
		// A coil has no contact form: P does nothing.
		await clickAt(b, await center(b, byId('coil', 'y')));
		await reset(b);
		await P(b);
		assert.deepEqual(await ldOps(b), []);
	});
});

test('Ladder edge contact: double-click a contact and type +Tag — the retag posts it whole; an edge retags too (L24, #213)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		const a = await center(b, byId('contact', 'a'));
		await b.dblclick(a.x, a.y);
		await sleep(200);
		assert.equal(await b.eval('document.activeElement?.value'), 'a');
		await b.eval(`document.activeElement.select()`);
		await reset(b);
		await typeText(b, '+Start');
		await enter(b);
		assert.deepEqual(await ldOps(b), [{ type: 'setRef', rung: 'r1', path: [0], ref: '+Start' }]);
		await deliver(b, { type: 'ldModel', model: EDGE, title: 'program.ld' });
		const e = await center(b, byId('edge', 'M1_StartPB'));
		await b.dblclick(e.x, e.y);
		await sleep(200);
		assert.equal(await b.eval('document.activeElement?.value'), 'M1_StartPB', 'the edit shows the bare tag (the form stays)');
		await b.eval(`document.activeElement.select()`);
		await reset(b);
		await typeText(b, 'M2_StartPB');
		await enter(b);
		assert.deepEqual(await ldOps(b), [{ type: 'setRef', rung: 'm1', path: [0], ref: 'M2_StartPB' }]);
	});
});

const paletteDisabled = (b, label) => b.eval(`[...document.querySelectorAll('.palette button')].find((x) => x.textContent.trim() === ${JSON.stringify(label)})?.disabled`);

test('Ladder rung copy: select a rung, Ctrl+C, select another, Ctrl+V — pasteRung below it; ⧉ is enabled only with something to copy (L25, #217)', async () => {
	await withPage(async (b) => {
		const model = withRung(LD, 0, () => ({ comment: 'motor one' }));
		await deliver(b, { type: 'ldModel', model, title: 'p.ld' });
		assert.equal(await paletteDisabled(b, '⧉'), true, 'nothing selected, nothing to copy');
		await clickAt(b, await center(b, rungName('r1')));
		assert.equal(await paletteDisabled(b, '⧉'), false, 'a selected rung is copyable');
		await reset(b);
		await ctrlKey(b, 'c');
		assert.deepEqual(await ldOps(b), [], 'a copy posts nothing');
		await clickAt(b, await center(b, rungName('r2')));
		await ctrlKey(b, 'v');
		const ops = await ldOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		const r1 = model.rungs[0];
		assert.deepEqual(ops[0], {
			type: 'pasteRung',
			name: 'r1',
			after: 'r2',
			body: { comment: 'motor one', elements: r1.elements, coils: r1.coils }
		});
	});
});

test('Ladder rung copy: the palette ⧉ then ⎘ pastes below the source when nothing is selected; ✂ cuts the rung (L25, #217)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await clickAt(b, await center(b, rungName('r2')));
		await reset(b);
		await clickAt(b, await paletteBtn(b, '⧉'));
		// A click on the empty ladder clears the selection.
		await b.eval(`document.querySelector('.ladder').click()`);
		await sleep(100);
		await clickAt(b, await paletteBtn(b, '⎘'));
		await sleep(500);
		let ops = await ldOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'pasteRung');
		assert.equal(ops[0].after, 'r2');
		await clickAt(b, await center(b, rungName('r1')));
		await reset(b);
		await clickAt(b, await paletteBtn(b, '✂'));
		ops = await ldOps(b);
		assert.deepEqual(ops, [{ type: 'deleteRung', rung: 'r1' }]);
	});
});

// ── declare: a block's pins, and a counter's type ─────────────────────────
const CTU = {
	name: 'CTU',
	prefix: 'c',
	powerIn: 'CU',
	powerOut: 'Q',
	pins: [
		{ name: 'CU', type: 'BOOL', dir: 'in' },
		{ name: 'R', type: 'BOOL', dir: 'in' },
		{ name: 'PV', type: 'INT', dir: 'in' },
		{ name: 'Q', type: 'BOOL', dir: 'out' },
		{ name: 'CV', type: 'INT', dir: 'out' }
	]
};

const declBtn = (b, name, section) =>
	center(b, `.declpop button.declbtn[data-name="${name}"][data-section="${section}"]`);
const declText = (b, name, section) =>
	b.eval(`document.querySelector('.declpop button.declbtn[data-name="${name}"][data-section="${section}"]')?.textContent.trim() ?? null`);

test('Ladder declare: a CTU\'s CV => Starts is offered as VAR_EXTERNAL : INT, not the seed\'s REAL (L26, #219)', async () => {
	await withPage(async (b) => {
		const model = {
			name: 'Conveyor',
			vars: [{ name: 'M1_Run', type: 'BOOL', section: 'VAR_EXTERNAL', line: 3 }, { name: 'CountReset', type: 'BOOL', section: 'VAR_EXTERNAL', line: 4 }],
			fbTypes: [CTU],
			tags: [{ name: 'M1_Starts', type: 'REAL', role: 'state' }],
			rungs: [
				{
					name: 'starts',
					line: 6,
					endLine: 7,
					elements: [
						{ kind: 'contact', ref: 'M1_Run' },
						{ kind: 'fb', inst: 'cStarts', type: 'CTU', args: 'R := CountReset, PV := 9999, CV => M1_Starts', powerIn: 'CU', powerOut: 'Q' }
					],
					coils: []
				}
			]
		};
		await deliver(b, { type: 'ldModel', model, title: 'program.ld' });
		await clickAt(b, await center(b, '.palette button.declare'));
		assert.equal(await declText(b, 'M1_Starts', 'VAR_EXTERNAL'), 'VAR_EXTERNAL : INT');
		assert.equal(await declText(b, 'M1_Starts', 'VAR'), 'VAR : INT');
		await reset(b);
		await clickAt(b, await declBtn(b, 'M1_Starts', 'VAR_EXTERNAL'));
		assert.deepEqual(await ldOps(b), [{ type: 'declareVar', name: 'M1_Starts', varType: 'INT', section: 'VAR_EXTERNAL' }]);
	});
});

// lib/motor.ld mid-build: the FUNCTION_BLOCK shell, one rung, no pins yet.
const MOTOR = {
	name: '',
	vars: [{ name: 'Start', type: 'BOOL', section: 'VAR_INPUT', line: 3, pou: 'MotorStarter' }],
	blocks: [{ name: 'MotorStarter', line: 1, endLine: 12, pins: [{ name: 'Start', type: 'BOOL', dir: 'in' }] }],
	rungs: [
		{
			name: 'run',
			line: 7,
			endLine: 8,
			pou: 'MotorStarter',
			elements: [{ kind: 'contact', ref: 'Start' }, { kind: 'contact', ref: 'Stop', neg: true }, { kind: 'fb', inst: 'tFail', type: 'TON', args: 'PT := T#3S', powerIn: 'IN', powerOut: 'Q' }],
			coils: [{ kind: 'coil', ref: 'Run' }]
		}
	]
};

test('Ladder declare: inside a FUNCTION_BLOCK the offer makes pins — a read is VAR_INPUT, a coil VAR_OUTPUT (L27, #214)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: MOTOR, title: 'motor.ld' });
		const title = await b.eval(`document.querySelector('.palette button.declare')?.getAttribute('title') ?? ''`);
		assert.match(title, /Stop/);
		assert.match(title, /Run/);
		assert.doesNotMatch(title, /Start|tFail/, 'a declared pin and an instance are not offered');
		await clickAt(b, await center(b, '.palette button.declare'));
		const primary = await b.eval(`Object.fromEntries([...document.querySelectorAll('.declpop button.declbtn.primary')].map((x) => [x.dataset.name, x.dataset.section]))`);
		assert.deepEqual(primary, { Stop: 'VAR_INPUT', Run: 'VAR_OUTPUT' });
		assert.equal(await b.eval(`document.querySelectorAll('.declpop button[data-section="VAR_EXTERNAL"]').length`), 0, 'no tags inside a block');
		await reset(b);
		await clickAt(b, await declBtn(b, 'Stop', 'VAR_INPUT'));
		assert.deepEqual(await ldOps(b), [{ type: 'declareVar', name: 'Stop', varType: 'BOOL', section: 'VAR_INPUT', block: 'MotorStarter' }]);
	});
});

const openVars = async (b) => {
	const btn = await b.eval(`(() => { const el = [...document.querySelectorAll('.bar button')].find((x) => x.textContent.trim() === 'vars'); const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
	await clickAt(b, btn);
	assert.equal(await b.eval(`!!document.querySelector('.addrow')`), true, 'the variables panel opens');
};

test('Ladder vars panel: a block\'s scope declares its pins (in → out → in/out → local), renames and deletes them (L27, #214)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: MOTOR, title: 'motor.ld' });
		await openVars(b);
		assert.equal(await b.eval(`document.querySelector('.scopehead')?.textContent.trim()`), 'FUNCTION_BLOCK MotorStarter');
		const badge = () => b.eval(`document.querySelector('.addrow button.badge').dataset.section`);
		assert.equal(await badge(), 'VAR_INPUT');
		const order = [];
		for (let i = 0; i < 4; i++) {
			await clickAt(b, await center(b, '.addrow button.badge'));
			order.push(await badge());
		}
		assert.deepEqual(order, ['VAR_OUTPUT', 'VAR_IN_OUT', 'VAR', 'VAR_INPUT']);
		await clickAt(b, await center(b, '.addrow button.badge')); // VAR_OUTPUT
		await clickAt(b, await center(b, '.addrow input.grow'));
		await typeText(b, 'Faulted');
		await reset(b);
		await enter(b);
		let ops = await ldOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual({ ...ops[0], varType: undefined }, { type: 'declareVar', name: 'Faulted', varType: undefined, section: 'VAR_OUTPUT', block: 'MotorStarter' });
		// Rename: double-click the pin's name, type, Enter.
		const nm = await center(b, `.rows .row[data-id="Start"] .name`);
		await b.dblclick(nm.x, nm.y);
		await sleep(150);
		assert.equal(await b.eval('document.activeElement?.classList.contains("rename")'), true);
		await b.eval(`document.activeElement.select()`);
		await reset(b);
		await typeText(b, 'StartReq');
		await enter(b);
		ops = await ldOps(b);
		assert.deepEqual(ops, [{ type: 'renameVar', name: 'Start', newName: 'StartReq', block: 'MotorStarter' }]);
		// Delete it from its own block.
		await b.eval(`document.querySelector('.rows .row[data-id="Start"] .del').style.visibility = 'visible'`);
		await reset(b);
		await clickAt(b, await center(b, `.rows .row[data-id="Start"] .del`));
		assert.deepEqual(await ldOps(b), [{ type: 'deleteVar', name: 'Start', block: 'MotorStarter' }]);
	});
});

test('Ladder vars panel: rung-declared block instances are listed as inst : TYPE; Escape closes the panel (L28, #220)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: { ...EDGE, vars: [{ name: 'M1_Run', type: 'BOOL', section: 'VAR_EXTERNAL', line: 3 }] }, title: 'program.ld' });
		await openVars(b);
		const row = await b.eval(`(() => { const r = [...document.querySelectorAll('.rows .row')].find((x) => x.dataset.id === 'm1'); return r && { section: r.dataset.section, text: r.textContent.replace(/\\s+/g, ' ').trim() }; })()`);
		assert.ok(row, 'instance m1 is listed');
		assert.equal(row.section, 'instance');
		assert.match(row.text, /inst m1 : MotorStarter.*rung m1/);
		assert.equal(await b.eval(`document.querySelectorAll('.rows .row[data-section="instance"] .del').length`), 0, 'read-only: renamed on its rung');
		// Escape in the name field's suggestions stays there; on the panel it closes it.
		await reset(b);
		await esc(b);
		assert.equal(await b.eval(`!!document.querySelector('.addrow')`), false, 'Escape closes the variables panel');
		assert.deepEqual(await ldOps(b), []);
	});
});
