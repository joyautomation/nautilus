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
	ports() {
		return this.b.eval('JSON.stringify(window.__ports())').then(JSON.parse);
	}
	pipeFrom() {
		return this.b.eval('JSON.stringify(window.__pipeFrom())').then(JSON.parse);
	}
	pipeRows() {
		return this.b.eval('JSON.stringify(window.__pipeRows())').then(JSON.parse);
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
		// Count the page's animation frames from here: the echo re-renders on
		// the next frames, and software WebGL on CI takes its time, so wait
		// for the move rather than a fixed beat — and say how long it took and
		// how many frames the page managed, so a slow CI box is told apart
		// from a stalled one in the log.
		await s.b.eval('(() => { window.__frames = 0; (function f() { window.__frames++; requestAnimationFrame(f); })(); })()');
		const t0 = Date.now();
		const d = rigDoc();
		d.nodes[1].pos = [0.8, 0, 0.2];
		await s.deliver(d);
		let after = before;
		for (let i = 0; i < 200 && Math.abs(after.cx - before.cx) <= 20; i++) {
			await sleep(100);
			after = (await s.labels()).find((l) => l.title === 'P-101');
		}
		console.log(`S06: the label moved after ${Date.now() - t0} ms and ${await s.b.eval('window.__frames')} frames`);
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

test('S09 pipe: a click on a port, then another — ONE addPipe anchored at both ends with an orthogonal run; Esc cancels; the pipe lists and deletes', async () => {
	const doc = {
		...rigDoc(),
		kinds: {
			tank: { ports: { inlet: { at: [0, 0.47, 0], dir: [0, 1, 0] } } },
			pump: { ports: { discharge: { at: [0, 0.22, 0], dir: [0, 1, 0] }, suction: { at: [-0.22, 0.09, 0], dir: [-1, 0, 0] } } }
		}
	};
	await withScene(doc, async (s) => {
		const ports = await s.ports();
		assert.deepEqual(ports.map((p) => `${p.node}.${p.port}`).sort(), ['P101.discharge', 'P101.suction', 'T101.inlet'], JSON.stringify(ports));
		const pd = ports.find((p) => p.port === 'discharge');
		const ti = ports.find((p) => p.port === 'inlet');
		await s.b.click(pd.x, pd.y);
		await sleep(250);
		assert.deepEqual(await s.pipeFrom(), { node: 'P101', port: 'discharge' });
		assert.equal(await s.panelId(), null, 'a port click is not a node pick');
		assert.match(await s.b.eval('window.__footer()'), /pipe from P101\.discharge/);
		await s.b.pressKey('Escape');
		await sleep(150);
		assert.equal(await s.pipeFrom(), null, 'Esc cancels');
		await s.b.click(pd.x, pd.y);
		await sleep(250);
		await s.b.click(ti.x, ti.y);
		await sleep(250);
		const ops = await s.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'addPipe');
		assert.deepEqual(ops[0].from, { node: 'P101', port: 'discharge' });
		assert.deepEqual(ops[0].to, { node: 'T101', port: 'inlet' });
		// The run: up from the pump's discharge to a header above the tank's inlet lead, across, and down.
		assert.ok(ops[0].points.length >= 2, JSON.stringify(ops[0].points));
		const h = Math.max(...ops[0].points.map((p) => p[1]));
		assert.ok(h > 0.47 + 0.1, `header above the inlet's lead: ${h}`);
		assert.equal(await s.pipeFrom(), null, 'done drawing');
		// Echoed back, the pipe lists in the panel and deletes from there.
		const d = { ...doc, pipes: [{ from: ops[0].from, to: ops[0].to, points: ops[0].points }] };
		await s.reset();
		await s.deliver(d);
		await sleep(400);
		const rows = await s.pipeRows();
		assert.equal(rows.length, 1);
		assert.match(rows[0], /P101\.discharge → T101\.inlet · \d+ between/);
		const del = await s.b.eval(`JSON.stringify((() => { const b = document.querySelector('[data-kind=pipe-row] button.del'); const r = b.getBoundingClientRect(); return { cx: r.left + r.width / 2, cy: r.top + r.height / 2 }; })())`).then(JSON.parse);
		await s.b.click(del.cx, del.cy);
		await sleep(150);
		assert.deepEqual(await s.ops(), [{ type: 'deletePipe', index: 0 }]);
		assert.deepEqual(s.exceptions(), []);
	});
});

test('S10 anchored by name: ports "at top" of the bounds resolve to marks and a pipe runs between them', async () => {
	const doc = {
		...rigDoc(),
		kinds: { tank: { ports: { inlet: { at: { anchor: 'top' } } } }, pump: { ports: { discharge: { at: { anchor: 'top' } }, suction: { at: { anchor: 'left' } } } } }
	};
	await withScene(doc, async (s) => {
		const ports = await s.ports();
		assert.deepEqual(ports.map((p) => `${p.node}.${p.port}`).sort(), ['P101.discharge', 'P101.suction', 'T101.inlet'], JSON.stringify(ports));
		const pd = ports.find((p) => p.port === 'discharge');
		const ti = ports.find((p) => p.port === 'inlet');
		await s.b.click(pd.x, pd.y);
		await sleep(250);
		await s.b.click(ti.x, ti.y);
		await sleep(250);
		const ops = await s.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'addPipe');
		assert.deepEqual(ops[0].from, { node: 'P101', port: 'discharge' });
		assert.deepEqual(ops[0].to, { node: 'T101', port: 'inlet' });
		assert.deepEqual(s.exceptions(), []);
	});
});

/** S05's gizmo walk: from a world point, step the pointer out along the
 * projected +x until the controls report the X handle, then drag it
 * `px` pixels further out. */
async function dragGizmoX(s, origin, px = 120) {
	const project = (x, y, z) => s.b.eval(`JSON.stringify(window.__nxScene.project(${x}, ${y}, ${z}))`).then(JSON.parse);
	const axis = () => s.b.eval('window.__nxScene.gizmoAxis()');
	const [ox, oy] = await project(...origin);
	const [ax, ay] = await project(origin[0] + 0.5, origin[1], origin[2]);
	const len = Math.hypot(ax - ox, ay - oy);
	const dir = [(ax - ox) / len, (ay - oy) / len];
	let start = null;
	for (let d = 10; d <= 140; d += 5) {
		const x = ox + dir[0] * d;
		const y = oy + dir[1] * d;
		await s.b.moveTo(x, y);
		await sleep(30);
		if ((await axis()) === 'X') {
			start = { x, y };
			break;
		}
	}
	assert.ok(start, "the pointer found the gizmo's X handle along the projected +x axis");
	await s.b.mouseDown(start.x, start.y);
	await s.b.mouseMove(start.x + dir[0] * 20, start.y + dir[1] * 20);
	await s.b.mouseMove(start.x + dir[0] * px, start.y + dir[1] * px);
	await sleep(50);
	await s.b.mouseUp(start.x + dir[0] * px, start.y + dir[1] * px);
	await sleep(250);
}

test('S11 vertex drag: a pipe picked in the panel wears a handle per point; a click on one puts the gizmo on it; a drag posts ONE setPipePoints; Del removes the point; Esc', async () => {
	const doc = { ...rigDoc(), pipes: [{ id: 'run', points: [[0.2, 0.3, 0.9], [0.2, 0.8, 0.9], [1.2, 0.8, 0.9], [1.2, 0.3, 0.9]] }] };
	await withScene(doc, async (s) => {
		assert.deepEqual(await s.b.eval('JSON.stringify(window.__vertices())').then(JSON.parse), [], 'no handles until a pipe is selected');
		const row = await s.b.eval('JSON.stringify(window.__pipeRowButton(0))').then(JSON.parse);
		await s.b.click(row.cx, row.cy);
		await sleep(250);
		assert.equal(await s.b.eval('window.__selectedPipe()'), 0);
		const hs = await s.b.eval('JSON.stringify(window.__vertices())').then(JSON.parse);
		assert.equal(hs.length, 4, JSON.stringify(hs));
		const h1 = hs.find((h) => h.index === 1);
		await s.b.click(h1.x, h1.y);
		await sleep(250);
		assert.equal(await s.b.eval('window.__selectedVertex()'), 1, 'the handle took the click');
		assert.equal(await s.panelId(), null, 'and nothing was picked behind it');
		await dragGizmoX(s, [0.2, 0.8, 0.9]);
		const ops = await s.ops();
		assert.equal(ops.length, 1, 'one op per drag: ' + JSON.stringify(ops));
		assert.equal(ops[0].type, 'setPipePoints');
		assert.equal(ops[0].index, 0);
		assert.equal(ops[0].points.length, 4);
		assert.ok(ops[0].points[1][0] > 0.2, 'point 1 moved along +x: ' + JSON.stringify(ops[0].points[1]));
		assert.ok(Math.abs(ops[0].points[1][1] - 0.8) < 1e-6 && Math.abs(ops[0].points[1][2] - 0.9) < 1e-6, 'and only along x');
		assert.deepEqual(ops[0].points[0], [0.2, 0.3, 0.9], 'the others stayed');
		// Echo it back; the handle is where the point now is; Del drops the point.
		const d2 = { ...doc, pipes: [{ id: 'run', points: ops[0].points }] };
		await s.reset();
		await s.deliver(d2);
		await sleep(300);
		assert.equal(await s.b.eval('window.__selectedVertex()'), 1, 'the selection survives the echo');
		await s.b.pressKey('Delete');
		await sleep(150);
		assert.deepEqual(await s.ops(), [{ type: 'setPipePoints', index: 0, points: [ops[0].points[0], ops[0].points[2], ops[0].points[3]] }]);
		assert.equal(await s.b.eval('window.__selectedVertex()'), null, 'the removed point is no longer selected');
		assert.equal(await s.b.eval('window.__selectedPipe()'), 0, 'the pipe still is');
		// Pick a point again; Esc clears the point first, then the pipe.
		const h0 = (await s.b.eval('JSON.stringify(window.__vertices())').then(JSON.parse)).find((h) => h.index === 0);
		await s.b.click(h0.x, h0.y);
		await sleep(200);
		assert.equal(await s.b.eval('window.__selectedVertex()'), 0);
		await s.b.pressKey('Escape');
		await sleep(100);
		assert.equal(await s.b.eval('window.__selectedVertex()'), null, 'Esc clears the vertex');
		assert.equal(await s.b.eval('window.__selectedPipe()'), 0, 'but keeps the pipe');
		await s.b.pressKey('Escape');
		await sleep(100);
		assert.equal(await s.b.eval('window.__selectedPipe()'), null, 'a second Esc clears the pipe');
		assert.deepEqual(await s.b.eval('JSON.stringify(window.__vertices())').then(JSON.parse), []);
		assert.deepEqual(s.exceptions(), []);
	});
});

test('S12 re-route: moving a node whose anchored pipe still has the suggested run posts the move and the new run in ONE batch; a hand-laid run is left alone', async () => {
	const kinds = {
		tank: { ports: { inlet: { at: [0, 0.47, 0], dir: [0, 1, 0] } } },
		pump: { ports: { discharge: { at: [0, 0.22, 0], dir: [0, 1, 0] } } }
	};
	await withScene({ ...rigDoc(), kinds }, async (s) => {
		// Draw the pipe port to port, so its run is routePorts's own.
		const ports = await s.ports();
		const pd = ports.find((p) => p.port === 'discharge');
		const ti = ports.find((p) => p.port === 'inlet');
		await s.b.click(pd.x, pd.y);
		await sleep(200);
		await s.b.click(ti.x, ti.y);
		await sleep(250);
		const [add] = await s.ops();
		assert.equal(add.type, 'addPipe');
		const doc = { ...rigDoc(), kinds, pipes: [{ id: 'p', from: add.from, to: add.to, points: add.points }] };
		await s.reset();
		await s.deliver(doc);
		await sleep(300);
		// Select the tank (click its label) and drag it along +x.
		const tank = (await s.labels()).find((l) => l.title === 'T-101');
		await s.b.click(tank.cx, tank.cy);
		await sleep(250);
		assert.equal(await s.panelId(), 'T101');
		await dragGizmoX(s, [1.2, 0, 0.4]);
		const ops = await s.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'batch', 'the move and the re-route travel together');
		assert.equal(ops[0].ops[0].type, 'moveNode');
		assert.equal(ops[0].ops[0].id, 'T101');
		assert.equal(ops[0].ops[1].type, 'setPipePoints');
		assert.equal(ops[0].ops[1].index, 0);
		const newX = ops[0].ops[0].pos[0];
		assert.ok(newX > 1.2, 'moved along +x');
		const last = ops[0].ops[1].points[ops[0].ops[1].points.length - 1];
		assert.ok(Math.abs(last[0] - newX) < 1e-6, `the run's last leg drops at the tank's new x: ${JSON.stringify(last)} vs ${newX}`);
		// A run someone edited is not re-routed: the move goes alone.
		const tweaked = { ...doc, pipes: [{ ...doc.pipes[0], points: [[0.2, 0.9, 0.2], [1.2, 0.9, 0.4]] }] };
		await s.reset();
		await s.deliver(tweaked);
		await sleep(300);
		const tank2 = (await s.labels()).find((l) => l.title === 'T-101');
		await s.b.click(tank2.cx, tank2.cy);
		await sleep(250);
		await dragGizmoX(s, [1.2, 0, 0.4]);
		const ops2 = await s.ops();
		assert.equal(ops2.length, 1, JSON.stringify(ops2));
		assert.equal(ops2[0].type, 'moveNode', 'a hand-laid run stays: ' + JSON.stringify(ops2[0]));
		assert.deepEqual(s.exceptions(), []);
	});
});
