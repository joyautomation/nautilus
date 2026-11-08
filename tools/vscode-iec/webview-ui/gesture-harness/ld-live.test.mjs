// Ladder live actions on a selected element (INVENTORY C29, L32, L33): the
// webview tells the host which tag is selected (diagramSelection), T posts
// Toggle Bit for it (toggleBit), and contacts, edge contacts and coils carry
// the right-click context VS Code's webview/context menus key on
// (data-vscode-context, {"nautilusTag": …}). Elements in a FUNCTION_BLOCK
// body (its rungs carry a scope) and member or element references are not
// tags the controller's API can write, so they get none of it.
//
// What the extension host does with these messages (the write itself, its
// refusals, the Ctrl+T keybinding in the workbench) is covered by
// src/liveActions.test.ts, src/diagramSelection.test.ts and rig smoke
// check 23-toggle-bit. Real CDP input throughout.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, withPage, deliver, posted, reset, center, key, clickAt, byId, ldOps, sleep } from './diagram-helpers.mjs';

recordClips('ld-live');

const MODEL = {
	name: 'P',
	vars: [],
	rungs: [
		{
			name: 'r1',
			line: 5,
			endLine: 6,
			elements: [
				{ kind: 'contact', ref: 'StartPB' },
				{ kind: 'edge', ref: 'Trip', mode: 'P', trig: 'rt_r1_Trip' },
				{ kind: 'contact', ref: 'Pump.Ready' }
			],
			coils: [{ kind: 'coil', ref: 'Motor' }]
		},
		// A FUNCTION_BLOCK body's rung: its operands are the block's locals.
		{ name: 'fb1', line: 9, endLine: 10, scope: 'Seal', elements: [{ kind: 'contact', ref: 'In' }], coils: [{ kind: 'coil', ref: 'Out' }] }
	]
};

const T = (b) => key(b, 't', 'KeyT', 84);
const CTRL = process.platform === 'darwin' ? 4 : 2; // CDP modifiers: Meta 4, Ctrl 2
const CtrlT = (b) => key(b, 't', 'KeyT', 84, CTRL);

const selections = async (b) => (await posted(b)).filter((m) => m.type === 'diagramSelection').map((m) => m.tag);
const toggles = async (b) => (await posted(b)).filter((m) => m.type === 'toggleBit').map((m) => m.tag);
const ctxOf = (b, sel) => b.eval(`document.querySelector(${JSON.stringify(sel)})?.getAttribute('data-vscode-context') ?? null`);
// The element is located by its ref within a rung (ids are per-rung paths).
const el = (rung, ref) => `[data-rung="${rung}"][data-xref="${ref}"]`;

test('Ladder live: contacts, edge contacts and coils carry their tag as right-click context; FB locals and members do not (L33)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: MODEL, title: 'p.ld' });
		assert.deepEqual(JSON.parse(await ctxOf(b, el('r1', 'StartPB'))), { nautilusTag: 'StartPB' });
		assert.deepEqual(JSON.parse(await ctxOf(b, el('r1', 'Trip'))), { nautilusTag: 'Trip' });
		assert.deepEqual(JSON.parse(await ctxOf(b, el('r1', 'Motor'))), { nautilusTag: 'Motor' });
		assert.equal(await ctxOf(b, el('r1', 'Pump.Ready')), null, 'a member reference is not a tag the API writes');
		assert.equal(await ctxOf(b, el('fb1', 'In')), null, 'a FUNCTION_BLOCK local is not a tag');
		assert.equal(await ctxOf(b, el('fb1', 'Out')), null, 'a FUNCTION_BLOCK local is not a tag');
	});
});

test('Ladder live: selecting an element tells the host its tag; a member, an FB local or nothing selected tells it null (C29)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: MODEL, title: 'p.ld' });
		await reset(b);
		await clickAt(b, await center(b, el('r1', 'StartPB')));
		await clickAt(b, await center(b, el('r1', 'Motor')));
		await clickAt(b, await center(b, el('r1', 'Trip')));
		await clickAt(b, await center(b, el('r1', 'Pump.Ready')));
		await clickAt(b, await center(b, el('r1', 'StartPB')));
		await clickAt(b, await center(b, el('fb1', 'In')));
		await sleep(150);
		assert.deepEqual(await selections(b), ['StartPB', 'Motor', 'Trip', null, 'StartPB', null]);
	});
});

test('Ladder live: T on a selected contact or coil posts Toggle Bit for its tag, and no edit (L32)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: MODEL, title: 'p.ld' });
		await clickAt(b, await center(b, el('r1', 'StartPB')));
		await reset(b);
		await T(b);
		await T(b);
		await clickAt(b, await center(b, el('r1', 'Motor')));
		await T(b);
		await clickAt(b, await center(b, el('r1', 'Trip')));
		await T(b);
		await sleep(150);
		assert.deepEqual(await toggles(b), ['StartPB', 'StartPB', 'Motor', 'Trip']);
		assert.deepEqual(await ldOps(b), [], 'Toggle Bit is a live action, not an edit');
	});
});

test('Ladder live: T on a member reference or a FUNCTION_BLOCK local posts nothing (L32)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: MODEL, title: 'p.ld' });
		for (const [rung, ref] of [['r1', 'Pump.Ready'], ['fb1', 'In'], ['fb1', 'Out']]) {
			await clickAt(b, await center(b, el(rung, ref)));
			await reset(b);
			await T(b);
			await sleep(100);
			assert.deepEqual(await toggles(b), [], `${rung}/${ref}`);
			assert.deepEqual(await ldOps(b), [], `${rung}/${ref}: no edit either`);
		}
	});
});

test('Ladder live: a read-only diagram selects nothing, so T posts nothing; the right-click context is still there (L32, L33)', async () => {
	// Read-only (an L5X view, a diff): a click does not select, so the
	// right-click menu is the way to Toggle Bit / Set Live Value there.
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: MODEL, title: 'p.ld', readOnly: true });
		await reset(b);
		await clickAt(b, await center(b, el('r1', 'StartPB')));
		await T(b);
		await sleep(150);
		assert.deepEqual(await toggles(b), []);
		assert.deepEqual(await ldOps(b), []);
		assert.deepEqual(JSON.parse(await ctxOf(b, el('r1', 'StartPB'))), { nautilusTag: 'StartPB' });
	});
});

test('Ladder live: Ctrl+T posts no toggleBit from the webview (the workbench keybinding owns it), and no edit (L32)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: MODEL, title: 'p.ld' });
		await clickAt(b, await center(b, el('r1', 'StartPB')));
		await reset(b);
		await CtrlT(b);
		await sleep(150);
		assert.deepEqual(await toggles(b), [], 'one toggle per keypress: the webview must not also toggle on Ctrl+T');
		assert.deepEqual(await ldOps(b), []);
	});
});

test('Ladder live: T with nothing selected posts nothing', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: MODEL, title: 'p.ld' });
		await reset(b);
		await T(b);
		await sleep(100);
		assert.deepEqual(await toggles(b), []);
	});
});
