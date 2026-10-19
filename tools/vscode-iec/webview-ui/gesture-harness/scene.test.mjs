// Gesture coverage for the scene editor (src/scene/*): the production
// scene-editor bundle in headless Chrome with software WebGL, driven by
// REAL pointer and keyboard input, asserting on the ops it posts and the
// DOM it renders — the stage's labels, the palette, the node panel.
//
//   S01 open      a doc renders: the palette lists its kinds, the stage
//                 has a canvas, each node has a label
//   S02 place     arm a kind, click the ground: addNode at a snapped point
//                 on the ground plane, then the tool disarms
//   S03 pick      click a node's label-adjacent model: the panel shows its
//                 id; Escape deselects; Delete posts deleteNode
//   S04 panel     typing a position into the panel posts updateNode
//   S05 drag      the gizmo moves the node: ONE moveNode op on release,
//                 with the new position
//   S06 text      a doc echoed back with a changed pos moves the label
//
// Bundle under test: env SCENE_BUNDLE, else ../media/dist.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, copyFileSync, writeFileSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser } from './cdp.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const BUNDLE = process.env.SCENE_BUNDLE || join(HERE, '..', '..', 'media', 'dist');
const HEADLESS = process.env.HEADED !== '1';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** The rig's three built-ins on a 0.1 m grid, far enough apart to aim at. */
function rigDoc() {
	return {
		name: 'harness',
		camera: { pos: [2.2, 1.8, 2.6], target: [0.6, 0.1, 0] },
		grid: { pos: [0, 0, 0], size: [4, 4], cell: 0.1 },
		nodes: [
			{ id: 'T101', kind: 'tank', tag: 'T101', label: 'T-101', pos: [1.2, 0, 0.4] },
			{ id: 'P101', kind: 'pump', tag: 'P101', label: 'P-101', pos: [0.2, 0, 0.2] }
		]
	};
}

const KINDS = [
	{ name: 'tank', definedBy: 'builtin', type: 'Tank', members: ['Level', 'TempC'] },
	{ name: 'pump', definedBy: 'builtin', type: 'Motor', members: ['Running', 'Fault', 'Speed'] },
	{ name: 'valve', definedBy: 'builtin', type: 'Valve', members: ['Pos', 'Cmd'] }
];

class Scene {
	constructor(b) {
		this.b = b;
	}
	static async open(doc) {
		const runDir = mkdtempSync(join(tmpdir(), 'scene-run-'));
		writeFileSync(join(runDir, 'scene-host.html'), readFileSync(join(HERE, 'scene-host.html'), 'utf8'));
		copyFileSync(join(BUNDLE, 'scene-editor.js'), join(runDir, 'scene-editor.js'));
		copyFileSync(join(BUNDLE, 'scene-editor.css'), join(runDir, 'scene-editor.css'));
		const b = await Browser.launch({ headless: HEADLESS, webgl: true });
		const s = new Scene(b);
		await b.navigate('file://' + join(runDir, 'scene-host.html'));
		for (let i = 0; i < 100; i++) {
			if (await b.eval(`window.__posted().some((m) => m && m.type === 'sceneReady')`)) break;
			await sleep(50);
		}
		assert.ok(await b.eval(`window.__posted().some((m) => m && m.type === 'sceneReady')`), 'the bundle never announced sceneReady');
		await s.deliver(doc);
		for (let i = 0; i < 100; i++) {
			if (await b.eval('window.__ready()')) break;
			await sleep(50);
		}
		assert.ok(await b.eval('window.__ready()'), 'no palette + canvas after the doc arrived');
		// Labels are HTML overlays placed once the renderer has a frame.
		for (let i = 0; i < 100; i++) {
			if ((await s.labels()).length >= doc.nodes.length) break;
			await sleep(50);
		}
		await s.reset();
		return s;
	}
	async deliver(doc) {
		await this.b.eval(`window.__deliver(${JSON.stringify({ type: 'sceneDoc', doc, rendered: doc, kinds: KINDS, assets: [], title: 'harness.scene.json' })})`);
		await sleep(150);
	}
	ops() {
		return this.b.eval('JSON.stringify(window.__ops())').then(JSON.parse);
	}
	reset() {
		return this.b.eval('window.__reset()');
	}
	kinds() {
		return this.b.eval('JSON.stringify(window.__kinds())').then(JSON.parse);
	}
	labels() {
		return this.b.eval('JSON.stringify(window.__labels())').then(JSON.parse);
	}
	canvas() {
		return this.b.eval('JSON.stringify(window.__canvas())').then(JSON.parse);
	}
	panelId() {
		return this.b.eval('window.__panelId()');
	}
	exceptions() {
		return this.b.consoleLines.filter((l) => l.startsWith('[exception]'));
	}
	async close() {
		await this.b.close();
	}
}

async function withScene(doc, fn) {
	const s = await Scene.open(doc);
	try {
		return await fn(s);
	} finally {
		await s.close();
	}
}

/** A point on the canvas a little below a node's label: the label floats
 * over the node's top, so just under it is the model itself. */
const under = (label, dy = 40) => ({ x: label.cx, y: label.cy + dy });

test('S01 open: the doc renders — palette, canvas, a label per node', async () => {
	await withScene(rigDoc(), async (s) => {
		const kinds = await s.kinds();
		assert.deepEqual(kinds.map((k) => k.name), ['tank', 'pump', 'valve']);
		assert.ok(await s.canvas(), 'a WebGL canvas on stage');
		const labels = await s.labels();
		assert.deepEqual(labels.map((l) => l.title).sort(), ['P-101', 'T-101'], JSON.stringify(labels));
		assert.deepEqual(s.exceptions(), []);
	});
});

test('S02 place: arm a kind, click the ground — one addNode on the grid, then disarmed', async () => {
	await withScene(rigDoc(), async (s) => {
		const [tank] = await s.kinds();
		await s.b.click(tank.cx, tank.cy);
		await sleep(100);
		assert.ok((await s.kinds())[0].armed, 'the tank is armed');
		assert.match(await s.b.eval('window.__footer()'), /place a tank/);
		// Empty ground: the canvas's lower-left quarter has no node in it.
		const c = await s.canvas();
		await s.b.click(c.left + c.w * 0.25, c.top + c.h * 0.8);
		await sleep(200);
		const ops = await s.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'addNode');
		assert.equal(ops[0].kind, 'tank');
		assert.equal(ops[0].pos.length, 3);
		assert.equal(ops[0].pos[1], 0, 'dropped on the ground plane');
		for (const v of ops[0].pos) assert.ok(Math.abs(v / 0.1 - Math.round(v / 0.1)) < 1e-6, `snapped to 0.1 m: ${v}`);
		assert.ok(!(await s.kinds())[0].armed, 'placement disarms after a drop');
		assert.deepEqual(s.exceptions(), []);
	});
});

test('S03 pick: click a node — the panel shows it; Escape deselects; Delete posts deleteNode', async () => {
	await withScene(rigDoc(), async (s) => {
		const tank = (await s.labels()).find((l) => l.title === 'T-101');
		const p = under(tank);
		await s.b.click(p.x, p.y);
		await sleep(200);
		assert.equal(await s.panelId(), 'T101', 'the panel shows the picked node');
		await s.b.pressKey('Escape');
		await sleep(100);
		assert.equal(await s.panelId(), null, 'Escape deselects');
		await s.b.click(p.x, p.y);
		await sleep(200);
		assert.equal(await s.panelId(), 'T101');
		await s.b.pressKey('Delete');
		await sleep(100);
		assert.deepEqual(await s.ops(), [{ type: 'deleteNode', id: 'T101' }]);
		assert.deepEqual(s.exceptions(), []);
	});
});

test('S04 panel: a typed position posts updateNode with the new pos', async () => {
	await withScene(rigDoc(), async (s) => {
		const pump = (await s.labels()).find((l) => l.title === 'P-101');
		const p = under(pump);
		await s.b.click(p.x, p.y);
		await sleep(200);
		assert.equal(await s.panelId(), 'P101');
		const x = await s.b.eval(`(() => { const i = document.querySelector('aside[aria-label=Properties] input[aria-label="pos x"]'); const r = i.getBoundingClientRect(); return { cx: r.left + r.width / 2, cy: r.top + r.height / 2 }; })()`);
		await s.b.click(x.cx, x.cy);
		await s.b.eval(`document.querySelector('aside[aria-label=Properties] input[aria-label="pos x"]').select()`);
		await s.b.send('Input.insertText', { text: '0.9' });
		// Commit the field the way a user leaves it: focus moves on.
		await s.b.pressKey('Tab', { keyCode: 9 });
		await sleep(150);
		const ops = await s.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual(ops[0], { type: 'updateNode', id: 'P101', patch: { pos: [0.9, 0, 0.2] } });
		assert.deepEqual(s.exceptions(), []);
	});
});

test('S05 drag: the gizmo moves the node — one moveNode on release, at a new position', async () => {
	await withScene(rigDoc(), async (s) => {
		const tank = (await s.labels()).find((l) => l.title === 'T-101');
		const p = under(tank);
		await s.b.click(p.x, p.y);
		await sleep(250);
		assert.equal(await s.panelId(), 'T101');
		// The gizmo sits at the node's position; its x handle runs along +x
		// from there. Walk the pointer out along the projected +x direction
		// until the controls report the X axis under it (window.__nxScene,
		// the bundle's harness surface), then drag from that point.
		const project = (x, y, z) => s.b.eval(`JSON.stringify(window.__nxScene.project(${x}, ${y}, ${z}))`).then(JSON.parse);
		const axis = () => s.b.eval('window.__nxScene.gizmoAxis()');
		const [ox, oy] = await project(1.2, 0, 0.4);
		const [ax, ay] = await project(1.7, 0, 0.4);
		const len = Math.hypot(ax - ox, ay - oy);
		const dir = [(ax - ox) / len, (ay - oy) / len];
		let start = null;
		for (let d = 12; d <= 140; d += 6) {
			const x = ox + dir[0] * d;
			const y = oy + dir[1] * d;
			await s.b.moveTo(x, y);
			await sleep(30);
			if ((await axis()) === 'X') {
				start = { x, y };
				break;
			}
		}
		assert.ok(start, 'the pointer found the gizmo\'s X handle along the projected +x axis');
		await s.b.mouseDown(start.x, start.y);
		await s.b.mouseMove(start.x + dir[0] * 20, start.y + dir[1] * 20);
		await s.b.mouseMove(start.x + dir[0] * 120, start.y + dir[1] * 120);
		await sleep(50);
		await s.b.mouseUp(start.x + dir[0] * 120, start.y + dir[1] * 120);
		await sleep(250);
		const ops = await s.ops();
		assert.equal(ops.length, 1, 'one op per drag: ' + JSON.stringify(ops));
		assert.equal(ops[0].type, 'moveNode');
		assert.equal(ops[0].id, 'T101');
		assert.ok(ops[0].pos[0] > 1.2, 'moved along +x: ' + JSON.stringify(ops[0].pos));
		assert.equal(ops[0].pos[1], 0, 'stayed on the ground');
		assert.ok(Math.abs(ops[0].pos[2] - 0.4) < 1e-6, 'and on its z');
		assert.ok(ops[0].pos.every((v) => Math.abs(v / 0.1 - Math.round(v / 0.1)) < 1e-6), 'snapped: ' + JSON.stringify(ops[0].pos));
		assert.deepEqual(ops[0].rot, [0, 0, 0], 'a move writes no turn');
		assert.deepEqual(s.exceptions(), []);
	});
});

test('S06 text: a doc echoed back with a changed pos moves the label', async () => {
	await withScene(rigDoc(), async (s) => {
		const before = (await s.labels()).find((l) => l.title === 'P-101');
		const d = rigDoc();
		d.nodes[1].pos = [0.8, 0, 0.2];
		await s.deliver(d);
		await sleep(200);
		const after = (await s.labels()).find((l) => l.title === 'P-101');
		assert.ok(Math.abs(after.cx - before.cx) > 20, `the label moved: ${before.cx} -> ${after.cx}`);
		assert.deepEqual(s.exceptions(), []);
	});
});

test('S07 look: the toolbar previews a look without an edit; "Use … as the scene\'s look" posts setLook; the echo clears the preview', async () => {
	await withScene(rigDoc(), async (s) => {
		const btn = (text) => s.b.eval(`JSON.stringify(window.__button(${JSON.stringify(text)}))`).then(JSON.parse);
		assert.ok((await btn('Lit')).active, 'the document has no look: lit is on');
		const paper = await btn('Paper');
		await s.b.click(paper.cx, paper.cy);
		await sleep(400);
		assert.ok((await btn('Paper')).active, 'paper is on stage');
		assert.deepEqual(await s.ops(), [], 'a preview is not an edit');
		const use = await btn('Use paper');
		assert.ok(use, "the panel offers to write the look");
		await s.b.click(use.cx, use.cy);
		await sleep(200);
		assert.deepEqual(await s.ops(), [{ type: 'setLook', look: 'paper' }]);
		const d = rigDoc();
		d.look = 'paper';
		await s.deliver(d);
		await sleep(300);
		assert.equal(await btn('Use paper'), null, 'the document has the look now');
		assert.ok((await btn('Paper')).active);
		assert.ok(await s.canvas(), 'the stage is still drawing');
		assert.deepEqual(s.exceptions(), []);
	});
});

test('S08 pick through glass: a click on the pump inside a tank picks the pump; again cycles to the tank; a label picks its node', async () => {
	const d = rigDoc();
	d.nodes = [
		{ id: 'T1', kind: 'tank', label: 'T-1', pos: [0.6, 0, 0.2] },
		{ id: 'P1', kind: 'pump', label: 'P-1', pos: [0.6, 0, 0.2] }
	];
	await withScene(d, async (s) => {
		const pump = (await s.labels()).find((l) => l.title === 'P-1');
		const p = under(pump, 25);
		await s.b.click(p.x, p.y);
		await sleep(200);
		assert.equal(await s.panelId(), 'P1', 'the opaque pump behind the glass, not the glass');
		await s.b.click(p.x, p.y);
		await sleep(200);
		assert.equal(await s.panelId(), 'T1', 'a second click on the same spot goes one deeper');
		await s.b.click(p.x, p.y);
		await sleep(200);
		assert.equal(await s.panelId(), 'P1', 'and round again');
		const tank = (await s.labels()).find((l) => l.title === 'T-1');
		await s.b.click(tank.cx, tank.cy);
		await sleep(200);
		assert.equal(await s.panelId(), 'T1', 'the lettering picks the node it names');
		assert.deepEqual(await s.ops(), []);
		assert.deepEqual(s.exceptions(), []);
	});
});
