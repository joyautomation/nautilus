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
const echo = (ports) => ({ type: 'previewDoc', component: 'Tank', builtin: true, sidecar: 'harness.component.json', hasSidecar: true, ports });

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
		await ed.deliver(echo(ops[0]));
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
		await ed.deliver(echo(edited));
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
