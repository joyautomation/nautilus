// FBD and SFC live actions on a selected element (INVENTORY C29, F-LIVE,
// S-LIVE), the same three things as the ladder's (ld-live.test.mjs): the
// webview tells the host which tag is selected (diagramSelection), T posts
// Toggle Bit for it (toggleBit), and the elements that name a tag carry the
// right-click context VS Code's webview/context menus key on
// (data-vscode-context, {"nautilusTag": …}).
//
// FBD: variable chips (input reads and coils) of a PROGRAM; not literals,
// blocks, FB instances, or anything in a FUNCTION_BLOCK body (model.block).
// SFC: an association whose target is a declared variable or a manifest tag
// (not an ACTION, a new action or a constant), and a transition whose
// condition is one such variable (its context keeps nautilusSfcTransition).
//
// The host side (the write, refusals, Ctrl+T) is src/liveActions.test.ts,
// src/diagramSelection.test.ts and rig smoke check 23. Real CDP input.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, withPage, deliver, posted, reset, center, key, clickAt, node, byId, fbdOps, sfcOps, sleep } from './diagram-helpers.mjs';

recordClips('fbd-sfc-live');

const T = (b) => key(b, 't', 'KeyT', 84);
const CTRL = process.platform === 'darwin' ? 4 : 2;
const CtrlT = (b) => key(b, 't', 'KeyT', 84, CTRL);
const selections = async (b) => (await posted(b)).filter((m) => m.type === 'diagramSelection').map((m) => m.tag);
const toggles = async (b) => (await posted(b)).filter((m) => m.type === 'toggleBit').map((m) => m.tag);
const ctxOf = async (b, sel) => {
	const raw = await b.eval(`document.querySelector(${JSON.stringify(sel)})?.getAttribute('data-vscode-context') ?? null`);
	return raw === null ? null : JSON.parse(raw);
};

// ── FBD ───────────────────────────────────────────────────────────────────
// Motor := AND(StartPB, TRUE) and a TON instance fed by Pump.Ready.
const FBD = {
	name: 'Main',
	nodes: [
		{ id: 'c:Motor', kind: 'coil', label: 'Motor', layer: 2, line: 5 },
		{ id: 'b:c.Motor', kind: 'block', label: 'AND', inputs: ['IN1', 'IN2'], outputs: ['OUT'], layer: 1, line: 5 },
		{ id: 'v:StartPB', kind: 'input', label: 'StartPB', layer: 0, line: 5 },
		{ id: 'k:TRUE', kind: 'input', label: 'TRUE', src: 'TRUE', layer: 0, line: 5 },
		{ id: 'f:t1', kind: 'fb', label: 't1', type: 'TON', inputs: ['IN', 'PT'], outputs: ['Q', 'ET'], layer: 1, line: 6 },
		{ id: 'v:Pump.Ready', kind: 'input', label: 'Pump.Ready', layer: 0, line: 6 }
	],
	edges: [
		{ from: 'v:StartPB', to: 'b:c.Motor', toPin: 'IN1' },
		{ from: 'k:TRUE', to: 'b:c.Motor', toPin: 'IN2' },
		{ from: 'b:c.Motor', fromPin: 'OUT', to: 'c:Motor' },
		{ from: 'v:Pump.Ready', to: 'f:t1', toPin: 'IN' }
	],
	vars: []
};
const chip = (id) => `[data-kind="chip"][data-id="${id}"]`;

test('FBD live: variable chips carry their tag as right-click context; literals, blocks, FB instances and members do not (F-LIVE)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'main.fbd' });
		assert.deepEqual(await ctxOf(b, chip('v:StartPB')), { nautilusTag: 'StartPB' });
		assert.deepEqual(await ctxOf(b, chip('c:Motor')), { nautilusTag: 'Motor' });
		assert.equal(await ctxOf(b, chip('k:TRUE')), null, 'a literal');
		assert.equal(await ctxOf(b, chip('v:Pump.Ready')), null, 'a member');
		assert.equal(await ctxOf(b, '[data-id="b:c.Motor"]'), null, 'a block');
		assert.equal(await ctxOf(b, '[data-id="f:t1"]'), null, 'an FB instance');
	});
});

test('FBD live: selecting a chip tells the host its tag; T toggles it with no edit; a literal, a block or nothing selected tells it null (C29, F-LIVE)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'main.fbd' });
		await reset(b);
		await clickAt(b, await center(b, node('v:StartPB')));
		await T(b);
		await clickAt(b, await center(b, node('c:Motor')));
		await T(b);
		await clickAt(b, await center(b, node('k:TRUE')));
		await T(b);
		await clickAt(b, await center(b, node('b:c.Motor')));
		await T(b);
		await sleep(150);
		assert.deepEqual(await selections(b), ['StartPB', 'Motor', null]);
		assert.deepEqual(await toggles(b), ['StartPB', 'Motor'], 'T on the literal or the block posts nothing');
		assert.deepEqual(await fbdOps(b), [], 'Toggle Bit is a live action, not an edit');
	});
});

test("FBD live: Ctrl+T posts no toggleBit from the webview (the workbench keybinding owns it)", async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'main.fbd' });
		await clickAt(b, await center(b, node('v:StartPB')));
		await reset(b);
		await CtrlT(b);
		await sleep(150);
		assert.deepEqual(await toggles(b), []);
		assert.deepEqual(await fbdOps(b), []);
	});
});

test("FBD live: a FUNCTION_BLOCK body's chips are its pins and locals: no context, no selection tag, T posts nothing", async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: { ...FBD, name: 'Seal', block: true }, title: 'seal.fbd' });
		assert.equal(await ctxOf(b, chip('v:StartPB')), null);
		assert.equal(await ctxOf(b, chip('c:Motor')), null);
		await reset(b);
		await clickAt(b, await center(b, node('v:StartPB')));
		await T(b);
		await sleep(150);
		assert.deepEqual((await selections(b)).filter(Boolean), []);
		assert.deepEqual(await toggles(b), []);
	});
});

// ── SFC ───────────────────────────────────────────────────────────────────
const SFC = {
	name: 'Fill',
	vars: [
		{ name: 'FillValve', type: 'BOOL', section: 'VAR_EXTERNAL', line: 2 },
		{ name: 'StartPB', type: 'BOOL', section: 'VAR_EXTERNAL', line: 2 },
		{ name: 'Limit', type: 'BOOL', section: 'VAR CONSTANT', line: 3 }
	],
	tags: [{ name: 'Level_OK', type: 'BOOL' }],
	steps: [
		{
			id: 'st:Idle',
			name: 'Idle',
			initial: true,
			line: 5,
			endLine: 6,
			actions: [
				{ qualifier: 'N', target: 'FillValve', line: 6 },
				{ qualifier: 'N', target: 'Wash', line: 6 },
				{ qualifier: 'S', target: 'Rinse', line: 6 },
				{ qualifier: 'N', target: 'Limit', line: 6 }
			]
		},
		{ id: 'st:Run', name: 'Run', initial: false, line: 8, endLine: 9 },
		{ id: 'st:Done', name: 'Done', initial: false, line: 11, endLine: 12 }
	],
	trans: [
		{ id: 'tr:14', from: ['Idle'], to: ['Run'], cond: 'StartPB', kind: 'normal', line: 14, endLine: 15 },
		{ id: 'tr:16', from: ['Run'], to: ['Done'], cond: 'Level_OK AND NOT FillValve', kind: 'normal', line: 16, endLine: 17 }
	],
	actions: [{ id: 'ac:Wash', name: 'Wash', body: 'FillValve := TRUE;', line: 19, endLine: 21 }]
};
const assoc = (i) => byId('assoc', `st:Idle:${i}`);
const trans = (id) => byId('transition', id);

test('SFC live: variable associations and a one-variable transition carry their tag; ACTIONs, new actions, constants and expressions do not (S-LIVE)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: SFC, title: 'fill.sfc' });
		assert.deepEqual(await ctxOf(b, assoc(0)), { nautilusTag: 'FillValve' });
		assert.equal(await ctxOf(b, assoc(1)), null, 'an ACTION');
		assert.equal(await ctxOf(b, assoc(2)), null, 'a new action, no body yet');
		assert.equal(await ctxOf(b, assoc(3)), null, 'a constant');
		// A transition keeps its own context (Fire Transition) and gains the tag.
		assert.deepEqual(await ctxOf(b, `g.trans${trans('tr:14')}`), { nautilusSfcTransition: 't14', nautilusTag: 'StartPB' });
		assert.deepEqual(await ctxOf(b, `g.trans${trans('tr:16')}`), { nautilusSfcTransition: 't16' }, 'an expression is no one tag');
	});
});

test('SFC live: selecting an association or a transition tells the host its tag; T toggles it with no edit (C29, S-LIVE)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: SFC, title: 'fill.sfc' });
		await reset(b);
		await clickAt(b, await center(b, `${assoc(0)} .assoctarget`));
		await T(b);
		await clickAt(b, await center(b, `${assoc(1)} .assoctarget`));
		await T(b);
		await clickAt(b, await center(b, `g.trans${trans('tr:14')} .cond, g.trans${trans('tr:14')} text`));
		await T(b);
		await sleep(150);
		assert.deepEqual(await selections(b), ['FillValve', null, 'StartPB']);
		assert.deepEqual(await toggles(b), ['FillValve', 'StartPB'], 'T on the ACTION association posts nothing');
		assert.deepEqual(await sfcOps(b), [], 'Toggle Bit is a live action, not an edit');
	});
});

test('SFC live: Ctrl+T posts no toggleBit from the webview; a diff selects nothing for live actions', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: SFC, title: 'fill.sfc' });
		await clickAt(b, await center(b, `${assoc(0)} .assoctarget`));
		await reset(b);
		await CtrlT(b);
		await sleep(150);
		assert.deepEqual(await toggles(b), []);
		assert.deepEqual(await sfcOps(b), []);
		// The diff view: a review, so no selection tag and T does nothing.
		await deliver(b, { type: 'sfcDiff', base: SFC, head: SFC, title: 'fill.sfc' });
		await reset(b);
		await clickAt(b, await center(b, `${assoc(0)} .assoctarget`));
		await T(b);
		await sleep(150);
		assert.deepEqual((await selections(b)).filter(Boolean), []);
		assert.deepEqual(await toggles(b), []);
	});
});
