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
import { recordClips, applyThemeJs, Browser, sleep, open, withPage, deliver, posted, reset, center, text, key, del, esc, clickAt, ctrlClick, FBD, byId, node, fbdOps, edgePoint, LD, ldOps, paletteBtn, SFC, sfcOps, ctrlKey, FBD_SRC, sfcStepPt, ready, restores, wheel, zoomPct, zoomBtn, rect, stepCenter, LD_LONG, TALL, css, FBD_PID, btnByText, typeText } from './diagram-helpers.mjs';

// GESTURE_CLIPS=<dir> records each test's run as <dir>/diagram/NN-<slug>.mp4.
recordClips('diagram');

test('FBD: deleting two selected notes posts ONE batched deleteNode (ordinal ids)', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD, title: 'n.fbd' });
		await clickAt(b, await center(b, byId('comment', 'cm:0')));
		await ctrlClick(b, await center(b, byId('comment', 'cm:1')));
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
		await clickAt(b, await center(b, byId('chip', 'c:Y')));
		await reset(b);
		await del(b);
		const ops = await fbdOps(b);
		assert.deepEqual(ops, [{ type: 'deleteNode', nodes: ['c:Y'] }]);
	});
});

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
		// A loaded CI runner can lag the selection; the keys only move a
		// selected node, so wait for it rather than racing it.
		for (let i = 0; i < 50 && !(await b.eval(`!!document.querySelector(${JSON.stringify(node('cm:2'))})?.classList.contains('selected')`)); i++) await sleep(100);
		await reset(b);
		await key(b, 'ArrowRight', 'ArrowRight', 39);
		await key(b, 'ArrowRight', 'ArrowRight', 39);
		await key(b, 'ArrowDown', 'ArrowDown', 40);
		// The move persists 350 ms after the last key; poll instead of a fixed sleep.
		for (let i = 0; i < 50 && (await fbdOps(b)).length === 0; i++) await sleep(100);
		await sleep(600); // and let any (wrong) second batch show up
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
		assert.deepEqual(await fbdOps(b), [{ type: 'init', pou: 'Heater2' }]);
	});
});

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
		assert.deepEqual(await ldOps(b), [{ type: 'addRung', name: 'rung1', after: '', pou: 'Interlock' }]);
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

test('Ladder: the FB… picker names a TON / CTU instance with the first free name', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'ldModel', model: LD, title: 'p.ld' });
		await reset(b);
		// The palette's "FB…" opens the block picker (filter, then instance).
		for (const type of ['TON', 'CTU']) {
			await clickAt(b, await paletteBtn(b, 'FB…'));
			await typeText(b, type);
			await key(b, 'Enter', 'Enter', 13); // picks the type, focus → instance
			await key(b, 'Enter', 'Enter', 13); // inserts
		}
		const ops = await ldOps(b);
		assert.equal(ops[0].inst, 't2'); // t1 is taken by rung r1
		assert.equal(ops[1].inst, 'c2'); // c1 is a header variable
	});
});

test('Ladder: the declare offer covers a block call\'s arguments and => targets', async () => {
	await withPage(async (b) => {
		const model = {
			name: 'P',
			vars: [{ name: 'm101', type: 'MotorStarter', section: 'VAR', line: 3 }, { name: 'Start', type: 'BOOL', section: 'VAR', line: 4 }],
			rungs: [
				{
					name: 'p101start',
					line: 6,
					endLine: 7,
					elements: [
						{ kind: 'contact', ref: 'Start' },
						{ kind: 'fb', inst: 'm101', type: 'MotorStarter', args: 'Reset := ResetFaults, Run => MotorRun, T := T#5S', powerIn: 'Start', powerOut: 'Run' }
					],
					coils: []
				}
			]
		};
		await deliver(b, { type: 'ldModel', model, title: 'permissives.ld' });
		const title = await b.eval(`document.querySelector('.palette button.declare')?.getAttribute('title') ?? ''`);
		assert.match(title, /ResetFaults/);
		assert.match(title, /MotorRun/);
		assert.doesNotMatch(title, /m101|Start\b|T#5S|Reset\b/);
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
		const pt = await b.eval(`(() => { const el = document.querySelector('[data-kind="step"][data-id="st:Spare"] .stepname'); const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
		await b.dblclick(pt.x, pt.y);
		await sleep(200);
		assert.equal(await b.eval(`document.activeElement?.tagName`), 'INPUT', 'float editor should hold focus');
		await esc(b);
		await reset(b);
		await del(b);
		assert.deepEqual(await sfcOps(b), [{ type: 'deleteStep', step: 'st:Spare' }]);
	});
});

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

// The smoke suite's 06-zoom-keys gesture on an SFC chart: click empty
// canvas, then the keys — and again with a palette button holding focus
// (the palette grew buttons in #45/#51; none may swallow the zoom keys).
test('SFC zoom: Ctrl+= / Ctrl+- / Ctrl+0 / Ctrl+wheel with focus in the chart or on a palette button', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'sfcModel', model: SFC, title: 's.sfc' });
		assert.equal(await zoomPct(b), '100%');
		// The first step shows under the sticky palette on load.
		const pal = await rect(b, '.palette');
		const idle = await stepCenter(b, 'Idle');
		assert.ok(idle.y - 10 > pal.y + pal.h, 'first step clear of the palette ' + JSON.stringify({ idle, pal }));
		const pane = await rect(b, '.zpane .flow');
		await clickAt(b, { x: pane.x + pane.w - 60, y: pane.y + pane.h - 200 });
		await reset(b);
		await key(b, '=', 'Equal', 187, 2);
		await key(b, '=', 'Equal', 187, 2);
		assert.equal(await zoomPct(b), '144%');
		await key(b, '-', 'Minus', 189, 2);
		assert.equal(await zoomPct(b), '120%');
		await key(b, '0', 'Digit0', 48, 2);
		assert.equal(await zoomPct(b), '100%', 'fit never magnifies past 100%');
		const c = await stepCenter(b, 'Run');
		for (let i = 0; i < 3; i++) await wheel(b, c.x, c.y, -60);
		assert.ok(parseInt(await zoomPct(b)) > 100, 'Ctrl+wheel zoomed in: ' + (await zoomPct(b)));
		await key(b, '0', 'Digit0', 48, 2);
		// Focus on a palette button: the keys still reach the pane.
		await b.eval(`[...document.querySelectorAll('.palette button')].find((x) => !x.disabled).focus()`);
		await key(b, '=', 'Equal', 187, 2);
		assert.equal(await zoomPct(b), '120%');
		await key(b, '-', 'Minus', 189, 2);
		assert.equal(await zoomPct(b), '100%');
		assert.deepEqual(await sfcOps(b), [], 'zoom keys are not edits');
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

test('FBD palette: "function block" places a PID with every input open, next free name, output refs in the hint', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD_PID, title: 'n.fbd' });
		await clickAt(b, await btnByText(b, '.bar button', '+ add'));
		await clickAt(b, await btnByText(b, 'button.item', 'function block'));
		assert.equal(await b.eval(`document.activeElement?.classList.contains('fbfilter')`), true, 'filter focused');
		// The catalog, grouped: standard then project.
		assert.deepEqual(await b.eval(`[...document.querySelectorAll('.fbpick button.fbitem')].map((x) => x.dataset.type)`), ['TON', 'PID', 'Starter']);
		await typeText(b, 'PID');
		await key(b, 'Enter', 'Enter', 13); // picks PID, focus → instance
		assert.equal(await b.eval(`document.querySelector('.fbpick input.fbinst').value`), 'pid2', 'pid1 is taken');
		assert.equal(await b.eval(`document.querySelector('.fbpick input.fbargs').value`), 'AUTO := _, PV := _, SP := _');
		await b.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', key: 'a', code: 'KeyA', windowsVirtualKeyCode: 65, modifiers: 2 });
		await typeText(b, 'lic');
		assert.match(await b.eval(`document.querySelector('.fbpick .hint').textContent`), /outputs: lic\.CV, lic\.SAT_HI/);
		await reset(b);
		await key(b, 'Enter', 'Enter', 13);
		assert.deepEqual(await fbdOps(b), [{ type: 'insertStatement', text: 'lic : PID(AUTO := _, PV := _, SP := _)' }]);
		assert.equal(await b.eval(`document.querySelectorAll('.fbpick').length`), 0, 'palette closes');
	});
});

test('FBD palette: output reference takes an FB output as its source — one statement', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD_PID, title: 'n.fbd' });
		await clickAt(b, await btnByText(b, '.bar button', '+ add'));
		await clickAt(b, await btnByText(b, 'button.item', 'output reference'));
		const field = (k) => `[...document.querySelectorAll('label.field')].find((l) => l.querySelector('span')?.textContent.trim() === '${k}')?.querySelector('input')`;
		await clickAt(b, await b.eval(`(() => { const r = ${field('name')}.getBoundingClientRect(); return { x: r.left + 10, y: r.top + r.height / 2 }; })()`));
		await b.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', key: 'a', code: 'KeyA', windowsVirtualKeyCode: 65, modifiers: 2 });
		await typeText(b, 'SpeedRef');
		await clickAt(b, await b.eval(`(() => { const r = ${field('source')}.getBoundingClientRect(); return { x: r.left + 10, y: r.top + r.height / 2 }; })()`));
		await typeText(b, 'pid1.C');
		// The instance's outputs are offered as sources.
		assert.ok((await b.eval(`[...document.querySelectorAll('.suggest .list button, .suggest .list .item')].map((x) => x.textContent)`)).some((t) => t.includes('pid1.CV')));
		await b.eval(`(${field('source')}).value = 'pid1.CV', (${field('source')}).dispatchEvent(new Event('input', { bubbles: true }))`);
		await reset(b);
		await clickAt(b, await btnByText(b, '.actions button', 'insert'));
		assert.deepEqual(await fbdOps(b), [{ type: 'insertStatement', text: 'SpeedRef := pid1.CV' }]);
	});
});

test('FBD: double-clicking an FB header renames the instance; the body inspects it', async () => {
	await withPage(async (b) => {
		await deliver(b, { type: 'model', model: FBD_PID, title: 'n.fbd' });
		const typePt = await center(b, `${node('f:pid1')} .title .type`);
		await b.dblclick(typePt.x, typePt.y);
		await sleep(200);
		assert.match(await b.eval(`document.activeElement?.tagName`), /INPUT/);
		await b.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', key: 'a', code: 'KeyA', windowsVirtualKeyCode: 65, modifiers: 2 });
		await typeText(b, 'lic');
		await reset(b);
		await key(b, 'Enter', 'Enter', 13);
		assert.deepEqual(await fbdOps(b), [{ type: 'rename', node: 'f:pid1', newName: 'lic' }]);
	});
});
