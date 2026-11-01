// Gesture coverage for a component's ports, edited in the component preview
// pane (the editor for a {Name}.component.json since the standalone
// Component Editor was retired): P01 drag a dot, P05 Esc deselects, P07
// redo. The webview posts `previewSetPorts { ports }` (the FULL list) — the
// host patches the sidecar and re-sends a previewDoc.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recordClips, withPreview, previewOps, sleepMs, exceptions } from './mimic-helpers.mjs';

recordClips('component-ports');

// A kit built-in renders from the registry with no compile, so the render
// (and the dots over it) are deterministic in the harness.
const PORTS = () => [{ name: 'in', x: 0, y: 0.5 }, { name: 'out', x: 1, y: 0.5 }];
const DOC = () => ({ component: 'Tank', builtin: true, ports: PORTS() });
// The same, with an explicit exit direction on `out` (regression for #130).
const DOC_DIR = () => ({ component: 'Tank', builtin: true, ports: [{ name: 'in', x: 0, y: 0.5 }, { name: 'out', x: 1, y: 0.5, dir: 'up' }] });
// The host's previewDoc. After a ports write it carries `ack`: the seq of
// the previewSetPorts it confirms (a previewDoc for any other reason — a
// text undo, a hand edit — carries none).
const echo = (ports, ack) => ({ type: 'previewDoc', component: 'Tank', builtin: true, sidecar: 'harness.component.json', hasSidecar: true, ports, ...(ack === undefined ? {} : { ack }) });

/** Ports mode starts off; its header toggle turns the dots into handles. */
async function portsOn(ed) {
	await ed.b.eval(`document.querySelector('[data-kind="ports-toggle"]').click(), true`);
	await sleepMs(80);
}

const dots = (ed) =>
	ed.b.eval(`JSON.stringify([...document.querySelectorAll('circle.port')].map((c) => { const r = c.getBoundingClientRect(); return { id: c.dataset.id, cx: r.left + r.width / 2, cy: r.top + r.height / 2, sel: c.classList.contains('sel'), title: c.querySelector('title')?.textContent }; }))`).then(JSON.parse);
const boxRect = (ed) =>
	ed.b.eval(`(() => { const r = document.querySelector('svg.ports').getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height }; })()`);

test('P01 component: dragging a port dot posts the port at its new fraction, and the dot renders there', async () => {
	await withPreview(DOC(), async (ed) => {
		await portsOn(ed);
		const box = await boxRect(ed);
		const [inDot] = await dots(ed);
		// to the top edge, 3/4 across
		const tx = box.x + box.w * 0.75;
		const ty = box.y;
		await ed.b.mouseDown(inDot.cx, inDot.cy);
		await ed.b.mouseMove(inDot.cx + 20, inDot.cy - 10);
		await ed.b.mouseMove(tx, ty);
		await ed.b.mouseUp(tx, ty);
		await sleepMs(150);
		const ops = await previewOps(ed);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual(ops[0].map((p) => p.name), ['in', 'out']);
		assert.ok(Math.abs(ops[0][0].x - 0.75) < 0.01 && ops[0][0].y === 0, JSON.stringify(ops[0][0]));
		assert.deepEqual(ops[0][1], { name: 'out', x: 1, y: 0.5 }, 'the other port is untouched');
		// The host echoes the new doc back; the dot renders at the new spot.
		await ed.deliver(echo(ops[0], 1));
		await sleepMs(150);
		const moved = (await dots(ed))[0];
		assert.ok(Math.abs(moved.cx - tx) < 2 && Math.abs(moved.cy - ty) < 2, JSON.stringify({ moved, tx, ty }));
		assert.match(moved.title, /^in · 0\.75, 0/);
		assert.deepEqual(exceptions(ed), []);
	});
});

test('P01 component: dragging a port with an explicit dir keeps the dir (#130)', async () => {
	await withPreview(DOC_DIR(), async (ed) => {
		await portsOn(ed);
		const box = await boxRect(ed);
		const out = (await dots(ed))[1];
		// to the bottom edge, 1/4 across
		const tx = box.x + box.w * 0.25;
		const ty = box.y + box.h;
		await ed.b.mouseDown(out.cx, out.cy);
		await ed.b.mouseMove(out.cx - 20, out.cy + 10);
		await ed.b.mouseMove(tx, ty);
		await ed.b.mouseUp(tx, ty);
		await sleepMs(150);
		const ops = await previewOps(ed);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		const moved = ops[0][1];
		assert.equal(moved.name, 'out');
		assert.ok(Math.abs(moved.x - 0.25) < 0.01 && Math.abs(moved.y - 1) < 0.01, JSON.stringify(moved));
		assert.equal(moved.dir, 'up', 'the explicit dir survives the drag: ' + JSON.stringify(moved));
		assert.deepEqual(ops[0][0], { name: 'in', x: 0, y: 0.5 }, 'the other port is untouched');
		assert.deepEqual(exceptions(ed), []);
	});
});

test('P01 component: a release no pointerup reached drops the dot where it was; a buttonless mouse does not drag it on', async () => {
	await withPreview(DOC(), async (ed) => {
		await portsOn(ed);
		const box = await boxRect(ed);
		const [inDot] = await dots(ed);
		// Drag to 1/4 across the top edge, then the button comes up where
		// no pointerup reaches the webview (outside it, after Chromium
		// dropped the capture): the next move arrives with no button down.
		const ax = box.x + box.w * 0.25;
		const ay = box.y;
		await ed.b.mouseDown(inDot.cx, inDot.cy);
		await ed.b.mouseMove(inDot.cx + 20, inDot.cy - 10);
		await ed.b.mouseMove(ax, ay);
		await ed.b.mouseMove(box.x + box.w * 0.75, box.y + box.h, { buttons: 0 });
		await ed.b.mouseMove(box.x + box.w * 0.6, box.y + box.h * 0.8, { buttons: 0 });
		await sleepMs(150);
		const ops = await previewOps(ed);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.ok(Math.abs(ops[0][0].x - 0.25) < 0.01 && ops[0][0].y === 0, JSON.stringify(ops[0][0]));
		assert.deepEqual(exceptions(ed), []);
	});
});

test('P05 component: Esc deselects the selected port dot and posts nothing', async () => {
	await withPreview(DOC(), async (ed) => {
		await portsOn(ed);
		const [inDot] = await dots(ed);
		await ed.b.click(inDot.cx, inDot.cy);
		await sleepMs(100);
		assert.deepEqual((await dots(ed)).map((d) => d.sel), [true, false], 'click selects');
		await ed.b.pressKey('Escape', { code: 'Escape', keyCode: 27 });
		await sleepMs(100);
		assert.deepEqual((await dots(ed)).map((d) => d.sel), [false, false], 'Esc deselects');
		assert.equal(await ed.b.eval(`document.querySelectorAll('.port.sel').length`), 0);
		assert.deepEqual(await previewOps(ed), [], 'a bare select + Esc is not an edit');
	});
});

// The preview pane has NO undo/redo of its own: every ports gesture posts
// one whole-list edit the host writes to the sidecar, and Ctrl+Z / Ctrl+Y
// go through VS Code's text undo stack. So the webview must (a) post nothing for
// those keys and (b) re-render whatever the host re-sends after the text
// undo/redo.
test('P07 component: redo — keys post nothing; the dot follows the doc the host re-sends after undo and redo', async () => {
	await withPreview(DOC(), async (ed) => {
		await portsOn(ed);
		const box = await boxRect(ed);
		const [inDot] = await dots(ed);
		await ed.b.mouseDown(inDot.cx, inDot.cy);
		await ed.b.mouseMove(box.x + box.w * 0.5, box.y);
		await ed.b.mouseUp(box.x + box.w * 0.5, box.y);
		await sleepMs(150);
		const [edited] = await previewOps(ed);
		assert.ok(Math.abs(edited[0].x - 0.5) < 0.01 && edited[0].y === 0);
		await ed.deliver(echo(edited, 1));
		await sleepMs(100);
		const atEdit = (await dots(ed))[0];
		await ed.resetOps();

		const key = (k, code, kc, modifiers) => ed.b.pressKey(k, { code, keyCode: kc, modifiers });
		await key('z', 'KeyZ', 90, 2);
		await sleepMs(100);
		// host's text undo restores the original ports
		await ed.deliver(echo(PORTS()));
		await sleepMs(150);
		const atUndo = (await dots(ed))[0];
		assert.ok(Math.abs(atUndo.cx - inDot.cx) < 2 && Math.abs(atUndo.cy - inDot.cy) < 2, 'undo: dot back at its start');

		await key('y', 'KeyY', 89, 2);
		await key('z', 'KeyZ', 90, 2 | 8);
		await sleepMs(100);
		await ed.deliver(echo(edited));
		await sleepMs(150);
		const atRedo = (await dots(ed))[0];
		assert.ok(Math.abs(atRedo.cx - atEdit.cx) < 2 && Math.abs(atRedo.cy - atEdit.cy) < 2, 'redo: dot back at the edited spot');
		assert.deepEqual(
			await ed.b.eval('JSON.stringify(window.__posted().map((m) => m.type))').then(JSON.parse),
			[],
			'undo/redo keys are the host\'s: the webview posts no message of its own'
		);
		assert.deepEqual(exceptions(ed), []);
	});
});

// Part-anchored ports (the kit's data-part="…" elements): a dot dropped on a
// part of the drawing anchors to it — its x/y become fractions of the part,
// so it stays on the vessel wall when the equipment is resized.
test('P08 component: a port dropped on a part of the drawing anchors to that part', async () => {
	await withPreview(DOC(), async (ed) => {
		await portsOn(ed);
		const vessel = await ed.b.eval(
			`(() => { const r = document.querySelector('.box [data-part="vessel"]').getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height }; })()`
		);
		const [inDot] = await dots(ed);
		const tx = vessel.x + vessel.w / 2;
		const ty = vessel.y + vessel.h / 2;
		await ed.b.mouseDown(inDot.cx, inDot.cy);
		await ed.b.mouseMove(inDot.cx + 20, inDot.cy);
		await ed.b.mouseMove(tx, ty);
		await ed.b.mouseUp(tx, ty);
		await sleepMs(150);
		const ops = await previewOps(ed);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		const moved = ops[0][0];
		assert.equal(moved.part, 'vessel', JSON.stringify(moved));
		assert.ok(Math.abs(moved.x - 0.5) < 0.02 && Math.abs(moved.y - 0.5) < 0.02, JSON.stringify(moved));
		await ed.deliver(echo(ops[0], 1));
		await sleepMs(150);
		const dot = (await dots(ed))[0];
		assert.ok(Math.abs(dot.cx - tx) < 2 && Math.abs(dot.cy - ty) < 2, JSON.stringify({ dot, tx, ty }));
		assert.deepEqual(exceptions(ed), []);
	});
});

// The drag's coordinate readout sits beside the dot. Dragged toward the
// right edge of a NARROW pane (the pane opens beside the mimic editor, so
// its stage is a few hundred px wide), the readout reached past the stage,
// which is a centred `overflow: auto` grid: a scrollbar came up mid-drag,
// the stage re-centred, the box moved under the pointer, and the release
// was measured against the moved box — the dot landed off the cursor, and
// jumped again when the readout unmounted (the nightly rig's
// component_move_port, #470/#471). The readout must not reflow the stage.
test('P01 component: in a narrow pane, a dot dragged toward the right edge lands at the release point (the readout does not reflow the stage)', async () => {
	await withPreview({ component: 'Pump', builtin: true, ports: [{ name: 'in', x: 0, y: 0.5 }, { name: 'seal', x: 0.5, y: 0 }] }, async (ed) => {
		await ed.b.eval(`document.body.style.width = '460px', true`);
		await sleepMs(200);
		await portsOn(ed);
		const box = await boxRect(ed);
		const seal = (await dots(ed))[1];
		// Nearly to the right edge: the readout beside the dot then reaches
		// well past the stage, whatever spare room the stage has.
		const tx = box.x + box.w * 0.98;
		const ty = box.y + box.h * 0.3;
		await ed.b.mouseDown(seal.cx, seal.cy);
		for (let i = 1; i <= 10; i++) await ed.b.mouseMove(seal.cx + ((tx - seal.cx) * i) / 10, seal.cy + ((ty - seal.cy) * i) / 10);
		await sleepMs(100);
		const during = await boxRect(ed);
		const stage = JSON.parse(
			await ed.b.eval(`(() => { const s = document.querySelector('.stage'); return JSON.stringify({ sw: s.scrollWidth, cw: s.clientWidth, sh: s.scrollHeight, ch: s.clientHeight }); })()`)
		);
		assert.ok(stage.sw <= stage.cw && stage.sh <= stage.ch, 'the drag gives the stage nothing to scroll: ' + JSON.stringify(stage));
		// the readout still shows, beside the pointer, while dragging (the
		// pane's own one: PortsPanel's rows carry a span.coord too)
		const tag = await ed.b.eval(
			`(() => { const e = document.querySelector('.pane > .coord'); if (!e) return null; const r = e.getBoundingClientRect(); return { x: r.left, y: r.top, text: e.textContent.trim() }; })()`
		);
		assert.ok(tag && /^0\.\d+, 0\.\d+$/.test(tag.text) && Math.abs(tag.x - tx - 10) < 3 && Math.abs(tag.y - ty + 20) < 3, 'readout beside the pointer: ' + JSON.stringify({ tag, tx, ty }));
		await ed.b.mouseUp(tx, ty);
		await sleepMs(150);
		const after = await boxRect(ed);
		assert.ok(
			Math.abs(during.x - box.x) < 0.5 && Math.abs(during.y - box.y) < 0.5,
			'the box stays put while the dot is dragged: ' + JSON.stringify({ box, during })
		);
		const ops = await previewOps(ed);
		assert.equal(ops.length, 1, JSON.stringify(ops));
		const moved = ops[0][1];
		assert.equal(moved.name, 'seal');
		const fx = (tx - after.x) / after.w;
		const fy = (ty - after.y) / after.h;
		assert.ok(
			Math.abs(moved.x - fx) < 0.01 && Math.abs(moved.y - fy) < 0.01,
			'the port lands at the release point: ' + JSON.stringify({ moved, fx, fy })
		);
		assert.deepEqual(exceptions(ed), []);
	});
});

// Every ports edit posts the whole list and the host confirms each with a
// previewDoc read back from the sidecar. Two quick edits (a dir stepped
// twice from the keyboard, a rename right after a drag) put two writes in
// flight; the confirmation of the FIRST can arrive after the second edit,
// and it must not put the first edit's list back on screen — the next
// edit would be built from it and silently undo the second.
test('P09 component: a late confirmation of an earlier edit does not revert a newer one', async () => {
	await withPreview(DOC(), async (ed) => {
		const sel = `document.querySelectorAll('.row[data-kind=port] select')[1]`;
		const down = () => ed.b.pressKey('ArrowDown', { code: 'ArrowDown', keyCode: 40 });
		await ed.b.eval(`${sel}.focus(), true`);
		await down(); // out: auto -> left   (edit 1)
		await sleepMs(80);
		await down(); // out: left -> right  (edit 2)
		await sleepMs(80);
		const ops = await previewOps(ed);
		assert.deepEqual(ops.map((o) => o[1].dir), ['left', 'right'], JSON.stringify(ops));
		// the host's confirmation of edit 1, late
		await ed.deliver(echo(ops[0], 1));
		await sleepMs(120);
		assert.equal(await ed.b.eval(`${sel}.value`), 'right', 'the late confirmation of edit 1 put its dir back on screen');
		await down(); // right -> up   (edit 3, built from what is on screen)
		await sleepMs(80);
		const after = await previewOps(ed);
		assert.equal(after.length, 3, JSON.stringify(after));
		assert.equal(after[2][1].dir, 'up', 'edit 3 was built from the stale list: ' + JSON.stringify(after[2]));
		// once every edit is confirmed, the host's list is the truth again
		await ed.deliver(echo(after[2], 3));
		await sleepMs(120);
		assert.equal(await ed.b.eval(`${sel}.value`), 'up');
		await ed.deliver(echo(PORTS()));
		await sleepMs(120);
		assert.equal(await ed.b.eval(`${sel}.value`), '', 'a previewDoc with no edit in flight (a text undo) applies');
		assert.deepEqual(exceptions(ed), []);
	});
});
