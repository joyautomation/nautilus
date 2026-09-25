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
import { mkdtempSync, copyFileSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser } from './cdp.mjs';
import { applyThemeJs } from './themes.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const BUNDLE = process.env.DIAGRAM_BUNDLE || join(HERE, '../../media/dist');
const HEADLESS = process.env.HEADED !== '1';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function open({ state } = {}) {
	const dir = mkdtempSync(join(tmpdir(), 'diagram-run-'));
	// `state` seeds the webview state (vscode.getState) the bundle reads at
	// mount — a panel reopening with what it saved.
	const html = readFileSync(join(HERE, 'diagram-host.html'), 'utf8').replace(
		'window.__POSTED__ = [];',
		`window.__POSTED__ = []; window.__STATE__ = ${JSON.stringify(state ?? null)};`
	);
	writeFileSync(join(dir, 'host.html'), html);
	copyFileSync(join(BUNDLE, 'fbd-flow.js'), join(dir, 'fbd-flow.js'));
	copyFileSync(join(BUNDLE, 'fbd-flow.css'), join(dir, 'fbd-flow.css'));
	const b = await Browser.launch({ headless: HEADLESS });
	await b.navigate('file://' + join(dir, 'host.html'));
	return b;
}

async function withPage(fn, opts) {
	const b = await open(opts);
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

// ── clipboard, select-all, shortcut help (editor parity) ──────────────────
// Headless Chrome denies the system clipboard, so these exercise the
// in-webview fallback — the path a paste must never depend on the other.
async function ctrlKey(b, k) {
	await key(b, k, 'Key' + k.toUpperCase(), k.toUpperCase().charCodeAt(0), 2);
	await sleep(500); // readClip gives the system clipboard up to 400 ms
}
const FBD_SRC = 'PROGRAM Main\nFBD\n  Y := AND(A, B)\nEND_FBD\nEND_PROGRAM\n';

test('FBD: Ctrl+C / Ctrl+V duplicates in place; Ctrl+X deletes, and its paste re-creates from the snapshot', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd', source: FBD_SRC });
		await clickAt(b, await center(b, node('c:Y')));
		await ctrlClick(b, await center(b, node('b:c.Y')));
		await reset(b);
		await ctrlKey(b, 'c');
		assert.deepEqual(await fbdOps(b), [], 'a copy posts nothing');
		await ctrlKey(b, 'v');
		let ops = await fbdOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'duplicate');
		assert.deepEqual([...ops[0].nodes].sort(), ['b:c.Y', 'c:Y']);
		assert.equal(ops[0].text, undefined, 'same file, not cut: plain in-place duplicate');

		await reset(b);
		await ctrlKey(b, 'x');
		ops = await fbdOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'deleteNode');
		assert.deepEqual([...ops[0].nodes].sort(), ['b:c.Y', 'c:Y']);
		await reset(b);
		await ctrlKey(b, 'v');
		ops = await fbdOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'duplicate');
		assert.equal(ops[0].text, FBD_SRC, 'a cut pastes from the snapshot');
		assert.equal(ops[0].keepRefs, true);
	});
});

test('FBD: Ctrl+A selects every node', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd', source: FBD_SRC });
		await clickAt(b, await center(b, node('cm:0')));
		await ctrlKey(b, 'a');
		assert.equal(await b.eval(`document.querySelectorAll('.svelte-flow__node.selected').length`), FBD.nodes.length);
		assert.match(await text(b), new RegExp(`${FBD.nodes.length} selected`));
	});
});

test('FBD: clipboard keys inside the float editor stay the field’s', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd', source: FBD_SRC });
		const pt = await center(b, node('c:Y'));
		await clickAt(b, pt);
		await ctrlKey(b, 'c'); // something IS on the clipboard
		const note = await center(b, node('cm:0'));
		await b.dblclick(note.x, note.y);
		await sleep(200);
		assert.match(await b.eval(`document.activeElement?.tagName`), /INPUT|TEXTAREA/);
		await reset(b);
		await ctrlKey(b, 'v');
		await ctrlKey(b, 'x');
		await ctrlKey(b, 'a');
		assert.deepEqual(await fbdOps(b), []);
	});
});

const sfcStepPt = (b, name) =>
	b.eval(`(() => { const el = [...document.querySelectorAll('.step .stepname')].find((x) => x.textContent === ${JSON.stringify(name)}); if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);

test('SFC: Ctrl-click multi-selects steps; copy/paste posts ONE pasteSteps with the transitions between them', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: SFC, title: 's.sfc' });
		await clickAt(b, await sfcStepPt(b, 'Idle'));
		await ctrlClick(b, await sfcStepPt(b, 'Run'));
		assert.equal(await b.eval(`document.querySelectorAll('.step.selected').length`), 2);
		await reset(b);
		await ctrlKey(b, 'c');
		await ctrlKey(b, 'v');
		const ops = await sfcOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'pasteSteps');
		assert.deepEqual(ops[0].steps.map((s) => s.name), ['Idle', 'Run']);
		assert.ok(ops[0].steps.every((s) => Number.isFinite(s.x) && Number.isFinite(s.y)), 'copies are placed');
		assert.deepEqual(ops[0].trans, [{ from: ['Idle'], to: ['Run'], cond: 'go' }]);
	});
});

test('SFC: Ctrl+X cuts the selected steps (and the transitions between them) in one op; ⎘ pastes', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: SFC, title: 's.sfc' });
		await clickAt(b, await sfcStepPt(b, 'Idle'));
		await ctrlClick(b, await sfcStepPt(b, 'Run'));
		await reset(b);
		await ctrlKey(b, 'x');
		assert.deepEqual(await sfcOps(b), [{ type: 'deleteSelection', nodes: ['st:Idle', 'st:Run', 'tr:7'] }]);
		await reset(b);
		await clickAt(b, await paletteBtn(b, '⎘'));
		await sleep(500);
		const ops = await sfcOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'pasteSteps');
	});
});

test('SFC: Ctrl+A then Del deletes every step and transition as ONE op', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: SFC, title: 's.sfc' });
		await clickAt(b, await sfcStepPt(b, 'Spare'));
		await ctrlKey(b, 'a');
		assert.equal(await b.eval(`document.querySelectorAll('.step.selected').length`), 3);
		await reset(b);
		await del(b);
		assert.deepEqual(await sfcOps(b), [{ type: 'deleteSelection', nodes: ['st:Idle', 'st:Run', 'st:Spare', 'tr:7'] }]);
	});
});

test('"?" lists each editor’s keys; the hint line carries its full text as a tooltip', async () => {
	const cases = [
		[{ type: 'model', model: FBD, title: 'n.fbd' }, /Ctrl \+ A/],
		[{ type: 'ldModel', model: LD, title: 'p.ld' }, /Cycle a coil/],
		[{ type: 'sfcModel', model: SFC, title: 's.sfc' }, /Ctrl \/ Shift \+ click/],
		// the zoom keys are listed too (ZoomPane / FitController)
		[{ type: 'ldModel', model: LD, title: 'p.ld' }, /Ctrl \+ 0Fit the widest rung/],
		[{ type: 'sfcModel', model: SFC, title: 's.sfc' }, /Ctrl \+ wheel \/ pinchZoom around the pointer/],
		[{ type: 'model', model: FBD, title: 'n.fbd' }, /Ctrl \+ = \/ Ctrl \+ - \/ Ctrl \+ 0Zoom in \/ out \/ fit/]
	];
	for (const [msg, want] of cases) {
		await withPage(async (b) => {
			await deliver(b, msg);
			const hint = await b.eval(`(() => { const h = document.querySelector('.bar .hint'); return h && { text: h.textContent, title: h.title }; })()`);
			assert.ok(hint && hint.text.length > 20, 'hint rendered');
			assert.equal(hint.title, hint.text);
			await clickAt(b, await center(b, '.nx-help-btn'));
			assert.match(await b.eval(`document.querySelector('.nx-help-pop')?.textContent ?? ''`), want);
			await esc(b);
			assert.equal(await b.eval(`document.querySelectorAll('.nx-help-pop').length`), 0, 'Esc closes it');
		});
	}
});

test('SFC: the vars panel declares/deletes with the payload `naut sfc edit` accepts', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: { ...SFC, vars: [{ name: 'go', type: 'BOOL', section: 'VAR', line: 2 }] }, title: 's.sfc' });
		const varsBtn = await b.eval(`(() => { const el = [...document.querySelectorAll('.bar button')].find((x) => x.textContent.trim() === 'vars'); const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
		await clickAt(b, varsBtn);
		await reset(b);
		await clickAt(b, await center(b, '.popover .del'));
		const nameIn = await center(b, '.addrow input.grow');
		await clickAt(b, nameIn);
		for (const ch of 'Lvl') await b.send('Input.insertText', { text: ch });
		await key(b, 'Enter', 'Enter', 13);
		const ops = await sfcOps(b);
		assert.deepEqual(ops[0], { type: 'deleteVar', name: 'go' });
		assert.equal(ops[1].type, 'declareVar');
		assert.equal(ops[1].name, 'Lvl');
		assert.equal(ops[1].section, 'VAR_EXTERNAL');
		assert.ok(ops[1].varType);
	});
});

// ── the ready handshake ─────────────────────────────────────────────────
test('Ready: the webview says ready on mount (the host holds its posts until then)', async () => {
	await withPage(async (b) => {
		assert.deepEqual((await posted(b)).filter((m) => m.type === 'ready'), [{ type: 'ready' }]);
	});
});

// ── restore from saved webview state ────────────────────────────────────
// "Developer: Reload Webviews" / Reload Window / a hidden tab coming back:
// the bundle mounts with vscode.getState() already holding the last model
// message and re-shows it BEFORE the host replays. A throw on that path
// aborts the mount — no `ready`, so the host never replays and the panel
// stays blank for good (the FBD editor did exactly that: show() assigned
// state declared further down the component).
const ready = async (b) => (await posted(b)).filter((m) => m.type === 'ready');
async function restores(saved, check, zoom) {
	await withPage(
		async (b) => {
			await sleep(250);
			assert.deepEqual(await ready(b), [{ type: 'ready' }], 'mount must still say ready');
			await check(b);
		},
		{ state: { msg: saved, ...(zoom ? { zoom } : {}) } }
	);
}

test('Restore: FBD editor mounts from a saved model (with source) and renders it', async () => {
	await restores({ type: 'model', model: FBD, title: 'n.fbd', source: FBD_SRC }, async (b) => {
		assert.ok(await center(b, node('c:Y')), 'saved FBD model not rendered');
	});
});

test('Restore: legacy state (the bare model message) still restores', async () => {
	await withPage(
		async (b) => {
			await sleep(250);
			assert.equal((await ready(b)).length, 1);
			assert.ok(await center(b, node('b:c.Y')));
		},
		{ state: { type: 'model', model: FBD, title: 'n.fbd' } }
	);
});

test('Restore: FBD preview mounts from a saved diff and renders it', async () => {
	const head = { ...FBD, nodes: FBD.nodes.filter((n) => n.id !== 'cm:2') };
	await restores({ type: 'diff', base: FBD, head, title: 'n.fbd (HEAD ↔ working)' }, async (b) => {
		assert.ok(await center(b, node('c:Y')), 'saved FBD diff not rendered');
	});
});

test('Restore: Ladder mounts from a saved model (and zoom) and renders it', async () => {
	await restores(
		{ type: 'ldModel', model: LD, title: 'p.ld' },
		async (b) => {
			assert.match(await text(b), /r1/);
			assert.equal(await zoomPct(b), '125%');
		},
		{ ld: 1.25 }
	);
});

test('Restore: Ladder mounts from a saved diff', async () => {
	const head = { ...LD, rungs: LD.rungs.slice(0, 1) };
	await restores({ type: 'ldDiff', base: LD, head, title: 'p.ld' }, async (b) => {
		assert.match(await text(b), /r2/);
	});
});

test('Restore: SFC mounts from a saved model (and diff) and renders it', async () => {
	await restores({ type: 'sfcModel', model: SFC, title: 's.sfc' }, async (b) => {
		assert.ok(await stepCenter(b, 'Run'));
	});
	await restores({ type: 'sfcDiff', base: SFC, head: { ...SFC, steps: SFC.steps.slice(0, 2) }, title: 's.sfc' }, async (b) => {
		assert.ok(await stepCenter(b, 'Spare'));
	});
});

// ── zoom / pan / fit (Ladder + SFC) ─────────────────────────────────────
// ZoomPane draws the SVGs at width/height × zoom over an unscaled viewBox,
// so every hit-test stays in screen space; these pin that the gestures
// that do their own coordinate math (SFC drag / connect) divide the zoom
// back out, and that Ladder's elementFromPoint drops still land.
async function wheel(b, x, y, deltaY, modifiers = 2) {
	await b.moveTo(x, y);
	await b.send('Input.dispatchMouseEvent', { type: 'mouseWheel', x, y, deltaX: 0, deltaY, modifiers });
	await sleep(120);
}
const zoomPct = (b) => b.eval(`document.querySelector('.zpct')?.textContent`);
const zoomBtn = (b, label) => center(b, `.zctl button[aria-label="${label}"]`);
const rect = (b, sel, i = 0) =>
	b.eval(`(() => { const el = document.querySelectorAll(${JSON.stringify(sel)})[${i}]; if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height, cx: r.left + r.width / 2, cy: r.top + r.height / 2 }; })()`);
const stepCenter = (b, name) =>
	b.eval(`(() => { const el = [...document.querySelectorAll('.step')].find((g) => g.querySelector('.stepname')?.textContent === ${JSON.stringify(name)}); const r = el.querySelector('.box').getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2, w: r.width }; })()`);

// Enough rungs to scroll: a zoom can only hold its anchor still when the
// pane has room to scroll the magnified content under it.
const LD_LONG = {
	...LD,
	rungs: [
		...LD.rungs,
		...Array.from({ length: 10 }, (_, i) => ({
			name: `x${i}`,
			line: 20 + 2 * i,
			endLine: 21 + 2 * i,
			elements: [{ kind: 'contact', ref: `i${i}` }],
			coils: [{ kind: 'coil', ref: `o${i}` }]
		}))
	]
};

test('Ladder zoom: Ctrl+wheel zooms around the cursor, persists in webview state, posts no op', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD_LONG, title: 'p.ld' });
		assert.equal(await zoomPct(b), '100%');
		const before = await rect(b, '.node', 1); // the TON block
		const w0 = (await rect(b, '.rsvg')).w;
		await reset(b);
		for (let i = 0; i < 4; i++) await wheel(b, before.cx, before.cy, -60);
		const after = await rect(b, '.node', 1);
		const z = parseInt(await zoomPct(b)) / 100;
		assert.ok(z > 1.5, `zoomed in (${z})`);
		assert.ok(after.w / before.w > 1.5, 'the block grew');
		// The point under the cursor stayed under the cursor.
		assert.ok(Math.abs(after.cx - before.cx) < 3 && Math.abs(after.cy - before.cy) < 3, JSON.stringify({ before, after }));
		assert.ok((await rect(b, '.rsvg')).w > w0, 'the canvas widened (scrolls)');
		assert.deepEqual(await ldOps(b), []);
		const st = await b.eval('window.__STATE__');
		assert.ok(Math.abs(st.zoom.ld - z) < 0.01, JSON.stringify(st.zoom));
		assert.equal(st.msg.type, 'ldModel', 'the model state survives beside the zoom');
		// A plain wheel still scrolls instead of zooming.
		await wheel(b, before.cx, before.cy, 100, 0);
		assert.equal(await zoomPct(b), Math.round(z * 100) + '%');
	});
});

test('Ladder zoom: buttons and Ctrl+= / Ctrl+- / Ctrl+0 (fit) with focus in the diagram', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await clickAt(b, await zoomBtn(b, 'zoom in'));
		assert.equal(await zoomPct(b), '120%');
		await clickAt(b, await zoomBtn(b, 'zoom out'));
		assert.equal(await zoomPct(b), '100%');
		// Focus the diagram (click a rung's background), then the keys.
		const names = await b.eval(`[...document.querySelectorAll('.rungname')].map((el) => { const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })`);
		await clickAt(b, names[0]);
		await reset(b);
		await key(b, '=', 'Equal', 187, 2);
		await key(b, '=', 'Equal', 187, 2);
		assert.equal(await zoomPct(b), '144%');
		await key(b, '-', 'Minus', 189, 2);
		assert.equal(await zoomPct(b), '120%');
		await key(b, '0', 'Digit0', 48, 2);
		assert.equal(await zoomPct(b), '100%', 'fit never magnifies past 100%');
		assert.deepEqual(await ldOps(b), [], 'zoom keys are not edits');
	});
});

test('Ladder zoom: a palette drop and a node drag still hit their spots at 173%', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		for (let i = 0; i < 3; i++) await clickAt(b, await zoomBtn(b, 'zoom in'));
		assert.equal(await zoomPct(b), '173%');
		// Zooming about the pane centre scrolled the left edge away.
		await b.eval(`document.querySelector('.zpane .flow').scrollTo(0, 0)`);
		await sleep(100);
		// The LAST spot of rung r2 (its coil slot) — dropping NC onto the
		// FIRST insert spot of r2 must insert at r2 index 0.
		const spots = await b.eval(`[...document.querySelectorAll('.spot')].map((el) => { const s = JSON.parse(el.getAttribute('data-spot')); const r = el.getBoundingClientRect(); return { rung: s.rung, op: s.spot.op, index: s.spot.index, x: r.left + r.width / 2, y: r.top + r.height / 2 }; })`);
		const target = spots.find((s) => s.rung === 'r2' && s.op === 'insert' && s.index === 0);
		assert.ok(target, JSON.stringify(spots));
		const from = await paletteBtn(b, '⊣/⊢');
		await reset(b);
		await b.mouseDown(from.x, from.y);
		await b.mouseMove(from.x + 20, from.y + 20);
		await b.mouseMove(target.x, target.y);
		await b.mouseUp(target.x, target.y);
		await sleep(150);
		const ops = await ldOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual({ type: ops[0].type, rung: ops[0].rung, neg: ops[0].neg, index: ops[0].index }, { type: 'insert', rung: 'r2', neg: true, index: 0 });
		// Drag r1's contact `a` onto the same spot: a move to r2 index 0.
		const a = await rect(b, '.node', 0);
		await reset(b);
		await b.mouseDown(a.cx, a.cy);
		await b.mouseMove(a.cx + 15, a.cy + 15);
		await b.mouseMove(target.x, target.y);
		await b.mouseUp(target.x, target.y);
		await sleep(150);
		const mv = await ldOps(b);
		assert.equal(mv.length, 1, JSON.stringify(mv));
		assert.equal(mv[0].type, 'move');
		assert.equal(mv[0].toRung, 'r2');
		assert.equal(mv[0].toIndex, 0);
	});
});

test('Ladder zoom: the float editor opens ON the element it edits', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		for (let i = 0; i < 2; i++) await clickAt(b, await zoomBtn(b, 'zoom in'));
		// Coil y hugs the right rail — past the pane's edge at 144%.
		await b.eval(`document.querySelectorAll('.node')[2].scrollIntoView({ block: 'center', inline: 'center' })`);
		await sleep(100);
		const c = await rect(b, '.node', 2); // coil y
		await b.dblclick(c.cx, c.cy);
		await sleep(200);
		const ed = await b.eval(`(() => { const el = document.activeElement; const r = el.getBoundingClientRect(); return { tag: el.tagName, value: el.value, x: r.left, y: r.top }; })()`);
		assert.equal(ed.tag, 'INPUT');
		assert.equal(ed.value, 'y');
		assert.ok(Math.abs(ed.x - c.x) < 12 && Math.abs(ed.y - c.y) < 30, JSON.stringify({ ed, c }));
		await esc(b);
	});
});

// A chart tall enough to overflow the 900px harness window.
const TALL = {
	name: 'Long',
	steps: Array.from({ length: 12 }, (_, i) => ({ id: `st:S${i}`, name: `S${i}`, initial: i === 0, line: 3 + 2 * i, endLine: 4 + 2 * i })),
	trans: Array.from({ length: 11 }, (_, i) => ({ id: `tr:${40 + i}`, from: [`S${i}`], to: [`S${i + 1}`], cond: 'go', kind: 'normal', line: 40 + i, endLine: 40 + i }))
};

test('SFC zoom: an overflowing chart fits on first load; a saved zoom is restored instead', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: TALL, title: 'long.sfc' });
		await sleep(150);
		const z = parseInt(await zoomPct(b)) / 100;
		assert.ok(z < 1 && z >= 0.5, `fitted, but no smaller than the legible 50% floor (${z})`);
		assert.ok(Math.abs((await b.eval('window.__STATE__')).zoom.sfc - z) < 0.01);
		// Ctrl+0 / the fit button go all the way: the whole chart shows.
		await clickAt(b, await zoomBtn(b, 'fit view'));
		const svg = await rect(b, 'svg.chart');
		const pane = await rect(b, '.zpane .flow');
		assert.ok(parseInt(await zoomPct(b)) / 100 <= z);
		assert.ok(svg.y + svg.h <= pane.y + pane.h + 1, 'the whole chart is visible ' + JSON.stringify({ svg, pane, z: await zoomPct(b) }));
	});
	await withPage(
		async (b) => {
			await deliver(b, { type: 'sfcModel', model: TALL, title: 'long.sfc' });
			await sleep(150);
			assert.equal(await zoomPct(b), '150%', 'the saved zoom wins over fit');
		},
		{ state: { zoom: { sfc: 1.5 } } }
	);
});

test('SFC zoom: step drag and connect rubber band stay under the cursor at 200%', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: SFC, title: 's.sfc' });
		for (let i = 0; i < 4; i++) await clickAt(b, await zoomBtn(b, 'zoom in'));
		assert.equal(await zoomPct(b), '207%');
		const z = 2.074;
		// Body drag: the pinned position moves by the SCREEN delta / zoom.
		// (At 207% Spare is below the fold: scroll it in first.)
		await b.eval(`[...document.querySelectorAll('.step')].find((g) => g.querySelector('.stepname')?.textContent === 'Spare').scrollIntoView({ block: 'center' })`);
		await sleep(100);
		const spare = await stepCenter(b, 'Spare');
		const before = await b.eval(`(() => { const g = [...document.querySelectorAll('.step')].find((g) => g.querySelector('.stepname')?.textContent === 'Spare'); const m = g.transform.baseVal[0].matrix; return { e: m.e, f: m.f }; })()`);
		await reset(b);
		await b.mouseDown(spare.x, spare.y);
		await b.mouseMove(spare.x + 50, spare.y + 20);
		await b.mouseMove(spare.x + 100, spare.y + 40);
		await b.mouseUp(spare.x + 100, spare.y + 40);
		await sleep(150);
		const ops = await sfcOps(b);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'setLayout');
		assert.ok(Math.abs(ops[0].x - (before.e + 100 / z)) <= 1.5, JSON.stringify({ op: ops[0], before }));
		assert.ok(Math.abs(ops[0].y - (before.f + 40 / z)) <= 1.5, JSON.stringify({ op: ops[0], before }));
		// Connect: drag Run's handle onto Idle; mid-drag the rubber band's
		// tip sits under the cursor.
		await deliver(b, { type: 'sfcModel', model: SFC, title: 's.sfc' });
		await b.eval(`document.querySelector('.zpane .flow').scrollTo(0, 0)`);
		await sleep(100);
		const handle = await b.eval(`(() => { const g = [...document.querySelectorAll('.step')].find((g) => g.querySelector('.stepname')?.textContent === 'Run'); const r = g.querySelector('.connect-handle').getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
		const idle = await stepCenter(b, 'Idle');
		await reset(b);
		await b.mouseDown(handle.x, handle.y);
		await b.mouseMove(handle.x + 30, handle.y + 10);
		await b.mouseMove(idle.x, idle.y);
		await sleep(80);
		const tip = await rect(b, '.rubberbandtip');
		assert.ok(Math.abs(tip.cx - idle.x) < 2 && Math.abs(tip.cy - idle.y) < 2, JSON.stringify({ tip, idle }));
		assert.ok(await b.eval(`!!document.querySelector('.step.connectTarget')`), 'Idle highlights as the drop target');
		await b.mouseUp(idle.x, idle.y);
		await sleep(150);
		assert.deepEqual(await sfcOps(b), [{ type: 'addTransition', from: ['Run'], to: ['Idle'], cond: 'TRUE' }]);
	});
});

// ── theme ───────────────────────────────────────────────────────────────
const css = (b, sel, prop) => b.eval(`(() => { const el = document.querySelector(${JSON.stringify(sel)}); return el && getComputedStyle(el).getPropertyValue(${JSON.stringify(prop)}).trim(); })()`);

test('Theme: xyflow colorMode follows the VS Code theme kind, live', async () => {
	await withPage(async (b) => {
		await b.eval(applyThemeJs('light'));
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		assert.ok(await b.eval(`document.querySelector('.svelte-flow').classList.contains('light')`));
		await b.eval(applyThemeJs('dark'));
		await sleep(150);
		assert.ok(await b.eval(`document.querySelector('.svelte-flow').classList.contains('dark')`));
		// MiniMap/Controls take the panel colours, not xyflow's white.
		assert.equal(await css(b, '.svelte-flow__minimap', 'background-color'), 'rgb(32, 32, 32)');
		assert.equal(await css(b, '.svelte-flow__controls-button', 'background-color'), 'rgb(32, 32, 32)');
		await b.eval(applyThemeJs('hc'));
		await sleep(150);
		assert.ok(await b.eval(`document.querySelector('.svelte-flow').classList.contains('dark')`));
	});
});

test('Theme: ladder diff colours come from theme tokens (light ≠ dark)', async () => {
	const base = { name: 'P', rungs: [LD.rungs[0]] };
	const head = { name: 'P', rungs: [LD.rungs[0], LD.rungs[1]] };
	const colors = {};
	for (const t of ['dark', 'light']) {
		await withPage(async (b) => {
			await b.eval(applyThemeJs(t));
			await deliver(b, { type: 'ldDiff', base, head, title: 'p.ld' });
			colors[t] = {
				legend: await css(b, '.legend.ld .sw.added', 'background-color'),
				bar: await css(b, '.rsvg.added .statusbar', 'fill')
			};
		});
	}
	assert.equal(colors.dark.legend, 'rgb(17, 168, 205)'); // terminal.ansiCyan (dark)
	assert.equal(colors.light.legend, 'rgb(5, 152, 188)'); // terminal.ansiCyan (light)
	assert.equal(colors.dark.bar, colors.dark.legend, 'rung bar and legend agree');
	assert.equal(colors.light.bar, colors.light.legend);
});

test('Theme: high contrast draws focus on the diagram surface', async () => {
	await withPage(async (b) => {
		await b.eval(applyThemeJs('hc'));
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await clickAt(b, await center(b, '.node'));
		assert.equal(await b.eval(`document.activeElement.classList.contains('wrap')`), true);
		assert.equal(await css(b, '.wrap', 'outline-color'), 'rgb(243, 133, 24)'); // contrastActiveBorder
		assert.equal(await css(b, '.wrap', 'outline-style'), 'solid');
	});
});

test('FBD zoom: Ctrl+= / Ctrl+- / Ctrl+0 drive the xyflow viewport too', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		await sleep(300);
		const scale = () => b.eval(`new DOMMatrix(getComputedStyle(document.querySelector('.svelte-flow__viewport')).transform).a`);
		const z0 = await scale();
		await clickAt(b, await center(b, node('c:Y')));
		await reset(b);
		// (A small diagram fits at the 2× max: go out first.)
		await key(b, '-', 'Minus', 189, 2);
		await sleep(400);
		const z1 = await scale();
		assert.ok(z1 < z0 * 0.95, `zoomed out ${z0} → ${z1}`);
		await key(b, '-', 'Minus', 189, 2);
		await sleep(400);
		await key(b, '=', 'Equal', 187, 2);
		await sleep(400);
		const z2 = await scale();
		assert.ok(z2 > z1 * 0.95 && z2 < z0, `zoomed back in ${z2}`);
		await key(b, '0', 'Digit0', 48, 2);
		await sleep(400);
		assert.ok(Math.abs((await scale()) - z0) < 0.02, 'Ctrl+0 fits again');
		assert.deepEqual(await fbdOps(b), []);
	});
});
