// Browser gesture regressions for the FBD / Ladder / SFC diagram webview:
// the PRODUCTION fbd-flow bundle in headless Chrome, models delivered the
// way the extension host delivers them (postMessage), real CDP input, and
// assertions on the exact messages the webview posts back.
//
// Run: `npm run test:gestures` (from webview-ui, after `npm run build`).
// Needs Chrome/Chromium on PATH — not part of `npm test` / CI.
//
// Bundle under test: env DIAGRAM_BUNDLE, else ../media/dist (the repo build).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, copyFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser } from './cdp.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const BUNDLE = process.env.DIAGRAM_BUNDLE || join(HERE, '../../media/dist');
const HEADLESS = process.env.HEADED !== '1';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function open() {
	const dir = mkdtempSync(join(tmpdir(), 'diagram-run-'));
	copyFileSync(join(HERE, 'diagram-host.html'), join(dir, 'host.html'));
	copyFileSync(join(BUNDLE, 'fbd-flow.js'), join(dir, 'fbd-flow.js'));
	copyFileSync(join(BUNDLE, 'fbd-flow.css'), join(dir, 'fbd-flow.css'));
	const b = await Browser.launch({ headless: HEADLESS });
	await b.navigate('file://' + join(dir, 'host.html'));
	return b;
}

async function withPage(fn) {
	const b = await open();
	try {
		await fn(b);
		assert.deepEqual(await b.eval('window.__errors'), [], 'page threw');
	} finally {
		await b.close();
	}
}

const deliver = async (b, msg) => {
	await b.eval(`window.__deliver(${JSON.stringify(msg)})`);
	await sleep(250);
};
const posted = (b) => b.eval('window.__POSTED__');
const reset = (b) => b.eval('window.__reset()');
const center = (b, sel) => b.eval(`window.__center(${JSON.stringify(sel)})`);
const text = (b) => b.eval('document.body.innerText');
async function key(b, k, code, keyCode, modifiers = 0) {
	await b.pressKey(k, { code, keyCode, modifiers });
	await sleep(120);
}
const del = (b) => key(b, 'Delete', 'Delete', 46);
const esc = (b) => key(b, 'Escape', 'Escape', 27);
async function clickAt(b, pt) {
	assert.ok(pt, 'target not rendered');
	await b.click(pt.x, pt.y);
	await sleep(120);
}
/** Click with Control held the way xyflow sees it (it tracks keydown, not
 * the event's modifier bits). */
async function ctrlClick(b, pt) {
	const base = { key: 'Control', code: 'ControlLeft', windowsVirtualKeyCode: 17, modifiers: 2 };
	await b.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', ...base });
	await b.click(pt.x, pt.y, { modifiers: 2 });
	await b.send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
	await sleep(120);
}

// `naut fbd graph` of a coil fed by AND(A, B) plus three comment notes.
const FBD = {
	name: 'Main',
	nodes: [
		{ id: 'c:Y', kind: 'coil', label: 'Y', layer: 2, line: 8 },
		{ id: 'b:c.Y', kind: 'block', label: 'AND', inputs: ['IN1', 'IN2'], outputs: ['OUT'], layer: 1, line: 8 },
		{ id: 'v:A', kind: 'input', label: 'A', layer: 0, line: 8 },
		{ id: 'v:B', kind: 'input', label: 'B', layer: 0, line: 8 },
		{ id: 'cm:0', kind: 'comment', label: 'note A', layer: 0, line: 6 },
		{ id: 'cm:1', kind: 'comment', label: 'note B', layer: 0, line: 9 },
		{ id: 'cm:2', kind: 'comment', label: 'note C', layer: 0, line: 11 }
	],
	edges: [
		{ from: 'v:A', to: 'b:c.Y', toPin: 'IN1' },
		{ from: 'v:B', to: 'b:c.Y', toPin: 'IN2' },
		{ from: 'b:c.Y', fromPin: 'OUT', to: 'c:Y' }
	],
	vars: []
};
const node = (id) => `.svelte-flow__node[data-id="${id}"]`;
const fbdOps = async (b) => (await posted(b)).filter((m) => m.type === 'edit').map((m) => m.op);

test('FBD: deleting two selected notes posts ONE batched deleteNode (ordinal ids)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		await clickAt(b, await center(b, node('cm:0')));
		await ctrlClick(b, await center(b, node('cm:1')));
		await reset(b);
		await del(b);
		const ops = await fbdOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'deleteNode');
		assert.deepEqual([...ops[0].nodes].sort(), ['cm:0', 'cm:1']);
	});
});

test('FBD: deleting a wired coil posts no disconnects for its own edges', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		await clickAt(b, await center(b, node('c:Y')));
		await reset(b);
		await del(b);
		const ops = await fbdOps(b);
		assert.deepEqual(ops, [{ type: 'deleteNode', nodes: ['c:Y'] }]);
	});
});

// A point ON an edge's path (its bbox center can miss a bent wire).
const edgePoint = (b, to, toPin) =>
	b.eval(`(() => {
		const g = [...document.querySelectorAll('.svelte-flow__edge')].find((el) => (el.getAttribute('data-id') ?? '').includes('|${to}|${toPin}|'));
		const path = g && (g.querySelector('path.svelte-flow__edge-interaction') ?? g.querySelector('path'));
		if (!path) return null;
		const p = path.getPointAtLength(path.getTotalLength() * 0.6);
		const m = path.getScreenCTM();
		return { x: p.x * m.a + p.y * m.c + m.e, y: p.x * m.b + p.y * m.d + m.f };
	})()`);

test('FBD: disconnecting two inputs of one block goes highest pin first', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		await clickAt(b, await edgePoint(b, 'b:c.Y', 'IN1'));
		await ctrlClick(b, await edgePoint(b, 'b:c.Y', 'IN2'));
		await reset(b);
		await del(b);
		const ops = await fbdOps(b);
		assert.deepEqual(ops.map((o) => `${o.type} ${o.toPin}`), ['disconnect IN2', 'disconnect IN1']);
	});
});

test('FBD: arrow-key moves persist as ONE setLayout once the keys settle', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		await clickAt(b, await center(b, node('cm:2')));
		await reset(b);
		await key(b, 'ArrowRight', 'ArrowRight', 39);
		await key(b, 'ArrowRight', 'ArrowRight', 39);
		await key(b, 'ArrowDown', 'ArrowDown', 40);
		await sleep(600);
		const ops = await fbdOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'setLayout');
		assert.equal(ops[0].entries.length, 1);
		assert.equal(ops[0].entries[0].node, 'cm:2');
	});
});

test('FBD: null arrays (older CLI) render without crashing; blank file offers initialize', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: { name: '', nodes: null, edges: null, blank: true }, title: 'heater-2.fbd' });
		assert.match(await text(b), /Empty file/);
		await reset(b);
		await clickAt(b, await center(b, '.blank button'));
		assert.deepEqual(await fbdOps(b), [{ type: 'init', pou: 'heater_2' }]);
	});
});

// ── Ladder ──────────────────────────────────────────────────────────────
const LD = {
	name: 'P',
	vars: [{ name: 'c1', type: 'BOOL', section: 'VAR', line: 3 }],
	rungs: [
		{
			name: 'r1',
			line: 5,
			endLine: 6,
			elements: [
				{ kind: 'contact', ref: 'a' },
				{ kind: 'fb', inst: 't1', type: 'TON', args: 'PT := T#1S', powerIn: 'IN', powerOut: 'Q' }
			],
			coils: [{ kind: 'coil', ref: 'y' }]
		},
		{ name: 'r2', line: 7, endLine: 8, elements: [{ kind: 'contact', ref: 'b' }], coils: [{ kind: 'coil', ref: 'z' }] }
	]
};
const ldOps = async (b) => (await posted(b)).filter((m) => m.type === 'ldEdit').map((m) => m.op);
const paletteBtn = (b, label) =>
	b.eval(`(() => { const el = [...document.querySelectorAll('.palette button')].find((x) => x.textContent.trim() === ${JSON.stringify(label)}); if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);

test('Ladder: empty body (rungs null) renders the palette; + rung works', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: { name: 'P', rungs: null }, title: 'p.ld' });
		await reset(b);
		await clickAt(b, await paletteBtn(b, '+ rung'));
		assert.deepEqual(await ldOps(b), [{ type: 'addRung', name: 'rung1', after: '' }]);
	});
});

test('Ladder: a blank file seeds with the file name on the first op', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: { name: '', rungs: [], blank: true }, title: 'interlock.ld' });
		await reset(b);
		await clickAt(b, await paletteBtn(b, '+ rung'));
		assert.deepEqual(await ldOps(b), [{ type: 'addRung', name: 'rung1', after: '', pou: 'interlock' }]);
	});
});

test('Ladder: click a rung name, Del deletes the rung', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		const names = await b.eval(`[...document.querySelectorAll('.rungname')].map((el) => { const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })`);
		await clickAt(b, names[1]);
		await reset(b);
		await del(b);
		assert.deepEqual(await ldOps(b), [{ type: 'deleteRung', rung: 'r2' }]);
	});
});

test('Ladder: TON from the palette takes the first free instance name', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await reset(b);
		await clickAt(b, await paletteBtn(b, 'TON'));
		await clickAt(b, await paletteBtn(b, 'CTU'));
		const ops = await ldOps(b);
		assert.equal(ops[0].inst, 't2'); // t1 is taken by rung r1
		assert.equal(ops[1].inst, 'c2'); // c1 is a header variable
	});
});

test('Ladder: Esc cancels an in-flight palette drag', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		const from = await paletteBtn(b, '⊣ ⊢');
		const spot = await center(b, '.spot');
		await reset(b);
		await b.mouseDown(from.x, from.y);
		await b.mouseMove(from.x + 30, from.y + 30);
		await b.mouseMove(spot.x, spot.y);
		await sleep(100);
		assert.ok(await b.eval(`!!document.querySelector('.ghost')`), 'drag ghost should show mid-drag');
		await esc(b);
		await b.mouseUp(spot.x, spot.y);
		await sleep(150);
		assert.deepEqual(await ldOps(b), []);
	});
});

test('Ladder: an L5X model is read-only — pill, no palette, no ops', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'Main.L5X', readOnly: true });
		assert.match(await text(b), /read-only · Logix export/);
		assert.equal(await b.eval(`document.querySelectorAll('.palette').length`), 0);
		assert.equal(await b.eval(`document.querySelectorAll('.spot').length`), 0);
		await reset(b);
		const names = await b.eval(`[...document.querySelectorAll('.rungname')].map((el) => { const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })`);
		await clickAt(b, names[0]);
		await del(b);
		assert.deepEqual(await ldOps(b), []);
	});
});

test('Ladder/SFC: a broken FIRST load keeps its own chrome (no FBD "+ add")', async () => {
	for (const lang of ['ld', 'sfc']) {
		await withPage(async (b) => {
			await deliver(b, { type: 'error', message: 'ld: line 3: boom', title: 'p.' + lang, lang });
			const t = await text(b);
			assert.match(t, /boom/);
			assert.doesNotMatch(t, /\+ add/);
			assert.doesNotMatch(t, /drag pin→pin/);
		});
	}
});

// ── SFC ─────────────────────────────────────────────────────────────────
const SFC = {
	name: 'Seq',
	steps: [
		{ id: 'st:Idle', name: 'Idle', initial: true, line: 3, endLine: 4 },
		{ id: 'st:Run', name: 'Run', initial: false, line: 5, endLine: 6 },
		{ id: 'st:Spare', name: 'Spare', initial: false, line: 9, endLine: 10 }
	],
	trans: [{ id: 'tr:7', from: ['Idle'], to: ['Run'], cond: 'go', kind: 'normal', line: 7, endLine: 8 }]
};
const sfcOps = async (b) => (await posted(b)).filter((m) => m.type === 'sfcEdit').map((m) => m.op);

test('SFC: empty chart (null arrays) — + step, first field focused, Enter adds the INITIAL step', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: { name: 'Seq', steps: null, trans: null }, title: 's.sfc' });
		await clickAt(b, await paletteBtn(b, '+ step'));
		assert.equal(await b.eval(`document.activeElement?.tagName`), 'INPUT');
		await reset(b);
		await key(b, 'Enter', 'Enter', 13);
		assert.deepEqual(await sfcOps(b), [{ type: 'addStep', name: 'Step1', initial: true }]);
		assert.equal(await b.eval(`document.querySelectorAll('.addform').length`), 0);
	});
});

test('SFC: Esc closes the add form', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: SFC, title: 's.sfc' });
		await clickAt(b, await paletteBtn(b, '+ step'));
		assert.equal(await b.eval(`document.querySelectorAll('.addform').length`), 1);
		await reset(b);
		await esc(b);
		assert.equal(await b.eval(`document.querySelectorAll('.addform').length`), 0);
		assert.deepEqual(await sfcOps(b), []);
	});
});

test('SFC: after the float editor closes, Del works without another click', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: SFC, title: 's.sfc' });
		const pt = await b.eval(`(() => { const el = [...document.querySelectorAll('.step .stepname')].find((x) => x.textContent === 'Spare'); const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
		await b.dblclick(pt.x, pt.y);
		await sleep(200);
		assert.equal(await b.eval(`document.activeElement?.tagName`), 'INPUT', 'float editor should hold focus');
		await esc(b);
		await reset(b);
		await del(b);
		assert.deepEqual(await sfcOps(b), [{ type: 'deleteStep', step: 'st:Spare' }]);
	});
});
