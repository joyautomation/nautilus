// Resize handles (R01–R03), part-anchored port drags (R04) and the canvas's
// own edge handles (R05–R06) in the mimic editor. Real CDP input against the
// production bundle; see README "Adding a suite".
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, twoTankDoc, withEditor, sleepMs, exceptions } from './mimic-helpers.mjs';

recordClips('mimic-resize');

const handle = (ed, edge) =>
	ed.b.eval(`(() => { const el = document.querySelector('.eq.sel .rs-${edge}'); if (!el) return null; const r = el.getBoundingClientRect(); return { cx: r.left + r.width / 2, cy: r.top + r.height / 2 }; })()`);
const handleCount = (ed) => ed.b.eval("document.querySelectorAll('.rs').length");
/** T1's box in canvas units (offsetWidth ignores the canvas scale). */
const size = (ed, id) =>
	ed.b.eval(`(() => { const el = document.querySelector('.eq[data-id="${id}"]'); return JSON.stringify([el.offsetWidth, el.offsetHeight]); })()`).then(JSON.parse);
/** Canvas units per viewport pixel. */
const unit = async (ed) => {
	const [a, b] = await Promise.all([ed.toVp(0, 0), ed.toVp(100, 0)]);
	return 100 / (b[0] - a[0]);
};
const resizes = async (ed) => (await ed.ops()).filter((o) => o.type === 'updateEquipment');

async function dragBy(ed, from, dx, dy) {
	const u = await unit(ed);
	await ed.b.mouseDown(from.cx, from.cy);
	await ed.b.mouseMove(from.cx + dx / u / 2, from.cy + dy / u / 2);
	await ed.b.mouseMove(from.cx + dx / u, from.cy + dy / u);
	await sleepMs(40);
	const live = await size(ed, 'T1');
	await ed.b.mouseUp(from.cx + dx / u, from.cy + dy / u);
	await sleepMs(80);
	return live;
}

test('R01 selecting one equipment shows three resize handles; none in pipe mode or with nothing selected', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		assert.equal(await handleCount(ed), 0);
		await ed.selectEquipment('T1');
		assert.equal(await handleCount(ed), 3);
		await ed.enterPipeMode();
		assert.equal(await handleCount(ed), 0);
	});
});

test('R02 dragging the corner handle resizes live and posts width + height (grid-snapped)', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.selectEquipment('T1');
		const [w0, h0] = await size(ed, 'T1');
		assert.equal(w0, 120);
		await ed.resetOps();
		const live = await dragBy(ed, await handle(ed, 'se'), 60, 40);
		const w = Math.round((w0 + 60) / 10) * 10;
		const h = Math.round((h0 + 40) / 10) * 10;
		assert.deepEqual(live, [w, h], 'the box follows the pointer mid-drag');
		assert.deepEqual(await resizes(ed), [{ type: 'updateEquipment', id: 'T1', patch: { width: w, height: h } }]);
		assert.deepEqual(await size(ed, 'T1'), [w, h], 'and stays there after release');
		assert.deepEqual(exceptions(ed), []);
	});
});

test('R03 the right-edge handle changes only the width, the bottom handle only the height', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.selectEquipment('T1');
		const [w0, h0] = await size(ed, 'T1');
		await ed.resetOps();
		await dragBy(ed, await handle(ed, 'e'), 80, 50);
		const [r1] = await resizes(ed);
		assert.deepEqual(r1.patch, { width: Math.round((w0 + 80) / 10) * 10, height: h0 });

		await ed.resetOps();
		await dragBy(ed, await handle(ed, 's'), 50, 60);
		const [r2] = await resizes(ed);
		assert.equal(r2.patch.width, r1.patch.width);
		assert.equal(r2.patch.height, Math.round((h0 + 60) / 10) * 10);
	});
});

test('R04 a part-anchored port drags off its part (anchors to the box) and back onto it (anchors to the part)', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.enterPortsMode('T1');
		const right = await ed.locate('port', 'right', '.porthandle');
		// T1 is 120 wide at (120, 180); its level scale fills the right ~26
		// units, so x = 238 is in the box but off the vessel.
		const [ox, oy] = await ed.toVp(238, 180 + 70);
		await ed.b.mouseDown(right.cx, right.cy);
		await ed.b.mouseMove(right.cx + 5, right.cy + 5);
		await ed.b.mouseMove(ox, oy);
		await ed.b.mouseUp(ox, oy);
		await sleepMs(80);
		let ops = (await ed.manifestOps()).filter((o) => o.type === 'setComponentPorts');
		assert.equal(ops.length, 1, JSON.stringify(ops));
		let moved = ops[0].ports.find((p) => p.name === 'right');
		assert.equal(moved.part, undefined, `off the vessel: no part, got ${JSON.stringify(moved)}`);
		assert.ok(Math.abs(moved.x - 118 / 120) < 0.01, JSON.stringify(moved));

		// Back onto the vessel's left wall: anchored to the vessel at x 0.
		await ed.deliver({ type: 'mimicManifest', components: { Tank: { ports: ops[0].ports } }, customComponents: [] });
		await sleepMs(80);
		const now = await ed.locate('port', 'right', '.porthandle');
		const vesselLeft = await ed.b.eval(`(() => { const r = document.querySelector('.eq[data-id="T1"] [data-part="vessel"]').getBoundingClientRect(); return JSON.stringify([r.left, r.top + r.height / 2]); })()`).then(JSON.parse);
		await ed.b.mouseDown(now.cx, now.cy);
		await ed.b.mouseMove(now.cx - 5, now.cy);
		await ed.b.mouseMove(vesselLeft[0] + 1, vesselLeft[1]);
		await ed.b.mouseUp(vesselLeft[0] + 1, vesselLeft[1]);
		await sleepMs(80);
		ops = (await ed.manifestOps()).filter((o) => o.type === 'setComponentPorts');
		moved = ops.at(-1).ports.find((p) => p.name === 'right');
		assert.equal(moved.part, 'vessel', JSON.stringify(moved));
		assert.ok(moved.x < 0.05 && Math.abs(moved.y - 0.5) < 0.05, JSON.stringify(moved));
		assert.deepEqual(exceptions(ed), []);
	});
});

// ── zoom (Z01–Z03) ─────────────────────────────────────────────────────────
const CTRL = 2;
const pct = (ed) => ed.b.eval("document.querySelector('.zctl .zpct').textContent").then((t) => parseInt(t, 10));
const eqCenter = (ed, id) =>
	ed.b.eval(`(() => { const r = document.querySelector('.eq[data-id="${id}"]').getBoundingClientRect(); return JSON.stringify([r.left + r.width / 2, r.top + r.height / 2]); })()`).then(JSON.parse);
const wheel = (ed, x, y, deltaY, modifiers = CTRL) => ed.b.send('Input.dispatchMouseEvent', { type: 'mouseWheel', x, y, deltaX: 0, deltaY, modifiers });

test('Z01 Ctrl+wheel zooms in around the pointer: the point under it stays put', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		const p0 = await pct(ed);
		const [cx, cy] = await eqCenter(ed, 'T2');
		await ed.b.moveTo(cx, cy);
		for (let i = 0; i < 3; i++) await wheel(ed, cx, cy, -120);
		await sleepMs(80);
		assert.ok((await pct(ed)) > p0, `zoomed in from ${p0}%`);
		const [x1, y1] = await eqCenter(ed, 'T2');
		assert.ok(Math.abs(x1 - cx) < 2 && Math.abs(y1 - cy) < 2, `T2 stayed under the pointer: ${[cx, cy]} -> ${[x1, y1]}`);
		// A plain wheel scrolls; it doesn't zoom.
		const p1 = await pct(ed);
		await wheel(ed, cx, cy, 120, 0);
		await sleepMs(60);
		assert.equal(await pct(ed), p1);
		assert.deepEqual(exceptions(ed), []);
	});
});

test('Z02 Ctrl+= and the + button zoom in, Ctrl+- out, Ctrl+0 and the fit button fit the pane again', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		const fit = await pct(ed);
		await ed.b.pressKey('=', { code: 'Equal', keyCode: 187, modifiers: CTRL });
		await sleepMs(40);
		const zin = await pct(ed);
		assert.ok(zin > fit, `${fit}% -> ${zin}%`);
		await ed.b.pressKey('-', { code: 'Minus', keyCode: 189, modifiers: CTRL });
		await ed.b.pressKey('-', { code: 'Minus', keyCode: 189, modifiers: CTRL });
		await sleepMs(40);
		assert.ok((await pct(ed)) < fit);
		await ed.b.pressKey('0', { code: 'Digit0', keyCode: 48, modifiers: CTRL });
		await sleepMs(40);
		assert.equal(await pct(ed), fit);
		const plus = await ed.b.eval(`(() => { const r = document.querySelector('.zctl button[aria-label="zoom in"]').getBoundingClientRect(); return JSON.stringify([r.left + r.width / 2, r.top + r.height / 2]); })()`).then(JSON.parse);
		await ed.b.click(...plus);
		await sleepMs(40);
		assert.ok((await pct(ed)) > fit);
		const fitBtn = await ed.b.eval(`(() => { const r = document.querySelector('.zctl button[aria-label="fit view"]').getBoundingClientRect(); return JSON.stringify([r.left + r.width / 2, r.top + r.height / 2]); })()`).then(JSON.parse);
		await ed.b.click(...fitBtn);
		await sleepMs(40);
		assert.equal(await pct(ed), fit);
	});
});

test('Z03 zoomed in, dragging equipment still moves it by canvas units', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		for (let i = 0; i < 3; i++) await ed.b.pressKey('=', { code: 'Equal', keyCode: 187, modifiers: CTRL });
		await sleepMs(60);
		await ed.selectEquipment('T1');
		await ed.resetOps();
		const [cx, cy] = await eqCenter(ed, 'T1');
		const u = await unit(ed);
		await ed.b.mouseDown(cx, cy);
		await ed.b.mouseMove(cx + 25 / u, cy);
		await ed.b.mouseMove(cx + 50 / u, cy + 20 / u);
		await ed.b.mouseUp(cx + 50 / u, cy + 20 / u);
		await sleepMs(80);
		const m = (await ed.ops()).filter((o) => o.type === 'moveEquipment');
		assert.deepEqual(m, [{ type: 'moveEquipment', id: 'T1', x: 170, y: 200 }]);
	});
});

// ── look + preview theme (L01–L02) ─────────────────────────────────────────
const canvasTheme = (ed) =>
	ed.b.eval("(() => { const c = document.querySelector('.canvas'); return JSON.stringify([c.dataset.theme, getComputedStyle(c).backgroundColor]); })()").then(JSON.parse);
const centerOf = (ed, sel) =>
	ed.b.eval(`(() => { const r = document.querySelector(${JSON.stringify(sel)}).getBoundingClientRect(); return JSON.stringify([r.left + r.width / 2, r.top + r.height / 2]); })()`).then(JSON.parse);

test('L01 the canvas previews in the kit palette, and the toolbar switch flips dark/light without an op', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		const [t0, bg0] = await canvasTheme(ed);
		assert.ok(t0 === 'dark' || t0 === 'light', t0);
		// the kit's --surface, not VS Code's panel colour
		assert.equal(bg0, t0 === 'dark' ? 'rgb(26, 26, 25)' : 'rgb(255, 255, 255)');
		await ed.resetOps();
		await ed.b.click(...(await centerOf(ed, 'header .theme-toggle')));
		await sleepMs(40);
		const [t1, bg1] = await canvasTheme(ed);
		assert.equal(t1, t0 === 'dark' ? 'light' : 'dark');
		assert.equal(bg1, t1 === 'dark' ? 'rgb(26, 26, 25)' : 'rgb(255, 255, 255)');
		// Tank's vessel outline follows: the kit's --edge in the new set
		const stroke = await ed.b.eval(`getComputedStyle(document.querySelector('.eq[data-id="T1"] [data-part="vessel"]')).stroke`);
		assert.equal(stroke, t1 === 'dark' ? 'rgb(122, 121, 115)' : 'rgb(126, 124, 116)');
		assert.deepEqual(await ed.ops(), [], 'a view setting: nothing written to the file');
	});
});

test('L02 the toolbar look picker writes setLook (null for the ISA-101 default)', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.resetOps();
		const pick = (v) =>
			ed.b.eval(`(() => { const s = document.querySelector('header .look select'); s.value = ${JSON.stringify(v)}; s.dispatchEvent(new Event('change', { bubbles: true })); })()`);
		await pick('realistic');
		await sleepMs(40);
		await pick('isa101');
		await sleepMs(40);
		assert.deepEqual(
			(await ed.ops()).filter((o) => o.type === 'setLook'),
			[{ type: 'setLook', look: 'realistic' }, { type: 'setLook', look: null }]
		);
	});
});

const canvasHandle = (ed, edge) =>
	ed.b.eval(`(() => { const el = document.querySelector('.crs-${edge}'); if (!el) return null; const r = el.getBoundingClientRect(); return { cx: r.left + r.width / 2, cy: r.top + r.height / 2 }; })()`);
/** The canvas's drawn size in canvas units (offsetWidth ignores the scale; minus the 1px border). */
const canvasSize = (ed) =>
	ed.b.eval(`(() => { const el = document.querySelector('.canvas'); return JSON.stringify([el.clientWidth, el.clientHeight]); })()`).then(JSON.parse);
const canvasOps = async (ed) => (await ed.ops()).filter((o) => o.type === 'setCanvas');

async function dragCanvasBy(ed, edge, dx, dy) {
	const from = await canvasHandle(ed, edge);
	const u = await unit(ed);
	await ed.b.mouseDown(from.cx, from.cy);
	await ed.b.mouseMove(from.cx + dx / u / 2, from.cy + dy / u / 2);
	await ed.b.mouseMove(from.cx + dx / u, from.cy + dy / u);
	await sleepMs(40);
	const live = await canvasSize(ed);
	const readout = await ed.b.eval("document.querySelector('.csize')?.textContent ?? null");
	await ed.b.mouseUp(from.cx + dx / u, from.cy + dy / u);
	await sleepMs(80);
	return { live, readout };
}

test('R05 dragging the canvas corner resizes it live, shows the size, and posts one setCanvas (grid-snapped)', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		assert.deepEqual(await canvasSize(ed), [900, 500]);
		await ed.resetOps();
		const { live, readout } = await dragCanvasBy(ed, 'se', 133, -97);
		assert.deepEqual(live, [1030, 400], 'the canvas follows the pointer mid-drag');
		assert.equal(readout, '1030 × 400');
		assert.deepEqual(await canvasOps(ed), [{ type: 'setCanvas', width: 1030, height: 400 }]);
		assert.deepEqual(await canvasSize(ed), [1030, 400], 'and stays there after release');
		assert.equal(await ed.b.eval("document.querySelector('.csize')"), null, 'the readout goes with the drag');
		assert.deepEqual(exceptions(ed), []);
	});
});

test('R06 the canvas right edge changes only the width, the bottom only the height; no handles in pipe mode', async () => {
	await withEditor(twoTankDoc(), async (ed) => {
		await ed.resetOps();
		await dragCanvasBy(ed, 'e', -200, 80);
		await dragCanvasBy(ed, 's', 80, 60);
		assert.deepEqual(await canvasOps(ed), [
			{ type: 'setCanvas', width: 700, height: 500 },
			{ type: 'setCanvas', width: 700, height: 560 }
		]);
		await ed.enterPipeMode();
		assert.equal(await ed.b.eval("document.querySelectorAll('.crs').length"), 0);
	});
});
