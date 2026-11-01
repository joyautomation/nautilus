// Kind editor gesture harness: the production scene-editor bundle, mounted
// as the KIND editor (<body data-scene-mode="kind">), in headless Chrome
// with software WebGL and real pointer input. The host side of the
// protocol (src/kindEditor.ts) is kind-host.html; the model is the
// package's own valve.glb, handed over as a data: URI the way the host
// hands over a webview URI. Asserts on the posted ops and on the DOM.
//
//   K01 open      the file renders: the model loads, its named objects
//                 fill the drive rows' mesh menus, the status previews
//   K02 drive     "Add drive" posts addDrive on the model's first object;
//                 a row's channel change posts updateDrive with a fresh drive
//   K03 field     a typed type posts setField; blank deletes
//   K04 samples   a sample value changes the status preview and posts nothing
//   K05 turntable the model turns while on, holds while off
//   K06 missing   a model the host did not find: no stage model, the panel
//                 says so, drives are still editable by name
//   K07 pick      the pointer over a part hovers it; a click picks it and
//                 "Add drive" starts on it; a chip picks by name; Esc and
//                 a click on the ground clear it; the part's drive row lights
//   K08 ports     a click on a part records where it landed; "Add port here"
//                 posts setPort there, facing out; a port in the file is a
//                 mark on the stage and a row in the panel; rename, delete
//   K09 anchors   a port "at top of Body" is a mark on the body; "Add port
//                 at…" posts a box point on the picked part; the label's
//                 mode switches to a part; a tint becomes a ramp with stops
//
// Bundle under test: env SCENE_BUNDLE, else ../media/dist.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, copyFileSync, writeFileSync, readFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser } from './cdp.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const BUNDLE = process.env.SCENE_BUNDLE || join(HERE, '..', '..', 'media', 'dist');
const HEADLESS = process.env.HEADED !== '1';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// The valve from hmi-3d's built-ins (Body, Handle, …), as the host would
// hand it over: a URI the webview can fetch. file:// pages cannot fetch a
// sibling file, so it travels as data:.
const GLB = join(HERE, '..', '..', '..', '..', 'hmi-3d', 'models', 'valve.glb');
const MODEL_URI = existsSync(GLB) ? 'data:model/gltf-binary;base64,' + readFileSync(GLB).toString('base64') : null;

function valveKind() {
	return {
		type: 'Valve',
		model: 'models/valve.glb',
		status: '{Pos:0} % {Cmd?open:shut}',
		drive: [{ mesh: 'Handle', turn: { axis: 'z', deg: { bind: 'Pos', scale: 0.9 } } }]
	};
}

class Kind {
	constructor(b) {
		this.b = b;
	}
	static async open(kind, { found = true } = {}) {
		const runDir = mkdtempSync(join(tmpdir(), 'kind-run-'));
		writeFileSync(join(runDir, 'kind-host.html'), readFileSync(join(HERE, 'kind-host.html'), 'utf8'));
		copyFileSync(join(BUNDLE, 'scene-editor.js'), join(runDir, 'scene-editor.js'));
		copyFileSync(join(BUNDLE, 'scene-editor.css'), join(runDir, 'scene-editor.css'));
		const b = await Browser.launch({ headless: HEADLESS, webgl: true });
		const k = new Kind(b);
		await b.navigate('file://' + join(runDir, 'kind-host.html'));
		for (let i = 0; i < 100; i++) {
			if (await b.eval(`window.__posted().some((m) => m && m.type === 'kindReady')`)) break;
			await sleep(50);
		}
		assert.ok(await b.eval(`window.__posted().some((m) => m && m.type === 'kindReady')`), 'the bundle never announced kindReady');
		await k.deliver(kind, { found });
		for (let i = 0; i < 100; i++) {
			if (await b.eval('window.__ready()')) break;
			await sleep(50);
		}
		assert.ok(await b.eval('window.__ready()'), 'no panel + canvas after the file arrived');
		if (found && kind.model) {
			for (let i = 0; i < 200; i++) {
				if ((await k.state()).startsWith('●')) break;
				await sleep(50);
			}
		}
		await k.reset();
		return k;
	}
	/** The host's kindDoc: the kind as written, the rendered copy with the
	 * model as a URI (when found), and the report of where it was. */
	async deliver(kind, { found = true } = {}) {
		const rendered = { ...kind };
		let model;
		if (kind.model) {
			if (found && MODEL_URI) {
				rendered.model = MODEL_URI;
				model = { pointer: '/model', path: kind.model, file: '/proj/hmi/static/' + kind.model };
			} else model = { pointer: '/model', path: kind.model };
		}
		await this.b.eval(`window.__deliver(${JSON.stringify({ type: 'kindDoc', kind, rendered, model, name: 'valve-x', title: 'valve-x.kind.json' })})`);
		await sleep(150);
	}
	ops() {
		return this.b.eval('JSON.stringify(window.__ops())').then(JSON.parse);
	}
	reset() {
		return this.b.eval('window.__reset()');
	}
	state() {
		return this.b.eval('window.__state()');
	}
	status() {
		return this.b.eval('window.__status()');
	}
	field(label) {
		return this.b.eval(`JSON.stringify(window.__field(${JSON.stringify(label)}))`).then(JSON.parse);
	}
	drives() {
		return this.b.eval('JSON.stringify(window.__drives())').then(JSON.parse);
	}
	button(text) {
		return this.b.eval(`JSON.stringify(window.__button(${JSON.stringify(text)}))`).then(JSON.parse);
	}
	rotation() {
		return this.b.eval('window.__rotation()');
	}
	labels() {
		return this.b.eval('JSON.stringify(window.__labels())').then(JSON.parse);
	}
	hover() {
		return this.b.eval('window.__hover()');
	}
	picked() {
		return this.b.eval('window.__picked()');
	}
	/** Where a part's centre is on the page (null until the model is on stage). */
	async partAt(name) {
		for (let i = 0; i < 40; i++) {
			const at = await this.b.eval(`JSON.stringify(window.__partAt(${JSON.stringify(name)}))`).then(JSON.parse);
			if (at) return at;
			await sleep(100);
		}
		return null;
	}
	parts() {
		return this.b.eval('JSON.stringify(window.__parts())').then(JSON.parse);
	}
	pickedLine() {
		return this.b.eval('window.__pickedLine()');
	}
	rowPicked(i) {
		return this.b.eval(`window.__rowPicked(${i})`);
	}
	hit() {
		return this.b.eval('JSON.stringify(window.__hit())').then(JSON.parse);
	}
	kports() {
		return this.b.eval('JSON.stringify(window.__kports())').then(JSON.parse);
	}
	portRows() {
		return this.b.eval('JSON.stringify(window.__portRows())').then(JSON.parse);
	}
	/** Type into a panel field and leave it (Tab), the way a value is committed. */
	async type(label, text) {
		const f = await this.field(label);
		assert.ok(f, `no field ${label}`);
		await this.b.click(f.cx, f.cy);
		await this.b.eval(`document.querySelector('aside[aria-label=Kind] [aria-label="${label}"]').select()`);
		await this.b.send('Input.insertText', { text });
		await this.b.pressKey('Tab', { keyCode: 9 });
		await sleep(150);
	}
	/** Pick an option in a panel <select>, the way a user would: set + change. */
	async choose(label, value, within = 'aside[aria-label=Kind]') {
		await this.b.eval(
			`(() => { const s = document.querySelector('${within} [aria-label="${label}"]'); s.value = ${JSON.stringify(value)}; s.dispatchEvent(new Event('change', { bubbles: true })); })()`
		);
		await sleep(150);
	}
	exceptions() {
		return this.b.consoleLines.filter((l) => l.startsWith('[exception]'));
	}
	async close() {
		await this.b.close();
	}
}

async function withKind(kind, opts, fn) {
	const k = await Kind.open(kind, opts);
	try {
		return await fn(k);
	} finally {
		await k.close();
	}
}

test('K01 open: the model loads, its objects fill the mesh menu, the status previews', { skip: !MODEL_URI && 'hmi-3d/models/valve.glb not in this checkout' }, async () => {
	await withKind(valveKind(), {}, async (k) => {
		assert.equal(await k.state(), '● model');
		const drives = await k.drives();
		assert.equal(drives.length, 1);
		assert.equal(drives[0].mesh, 'Handle');
		assert.equal(drives[0].channel, 'turn');
		assert.ok(drives[0].options.includes('Handle') && drives[0].options.includes('Body'), JSON.stringify(drives[0].options));
		// Samples default to 50 and true: "{Pos:0} % {Cmd?open:shut}" reads "50 % open".
		assert.equal(await k.status(), '50 % open');
		const labels = await k.labels();
		assert.ok(labels.some((l) => l.title === 'valve-x'), JSON.stringify(labels));
		assert.deepEqual(await k.ops(), []);
		assert.deepEqual(k.exceptions(), []);
	});
});

test('K02 drive: "Add drive" posts addDrive on the first object; a channel change posts a fresh updateDrive', { skip: !MODEL_URI && 'no model' }, async () => {
	await withKind(valveKind(), {}, async (k) => {
		const add = await k.button('Add drive');
		await k.b.click(add.cx, add.cy);
		await sleep(150);
		let ops = await k.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'addDrive');
		assert.equal(ops[0].drive.mesh, 'Body');
		assert.deepEqual(ops[0].drive.tint, { bind: 'Pos', on: 'running' });
		await k.reset();
		await k.choose('channel', 'spin', '[data-kind=drive-row][data-index="0"]');
		ops = await k.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual(ops[0], { type: 'updateDrive', index: 0, drive: { mesh: 'Handle', spin: { axis: 'y', revPerS: { bind: 'Pos', scale: 0.02 } } } });
		await k.reset();
		await k.choose('mesh', 'Body', '[data-kind=drive-row][data-index="0"]');
		ops = await k.ops();
		assert.deepEqual(ops[0], { type: 'updateDrive', index: 0, drive: { mesh: 'Body', turn: { axis: 'z', deg: { bind: 'Pos', scale: 0.9 } } } });
		assert.deepEqual(k.exceptions(), []);
	});
});

test('K03 field: a typed type posts setField; blank deletes', { skip: !MODEL_URI && 'no model' }, async () => {
	await withKind({ ...valveKind(), labelAt: [0, 0.2, 0] }, {}, async (k) => {
		await k.type('type', 'ControlValve');
		assert.deepEqual(await k.ops(), [{ type: 'setField', key: 'type', value: 'ControlValve' }]);
		await k.reset();
		await k.type('status', '');
		assert.deepEqual(await k.ops(), [{ type: 'setField', key: 'status', value: null }]);
		await k.reset();
		await k.type('label y', '0.3');
		assert.deepEqual(await k.ops(), [{ type: 'setField', key: 'labelAt', value: [0, 0.3, 0] }]);
		assert.deepEqual(k.exceptions(), []);
	});
});

test('K04 samples: a sample value changes the status preview and posts nothing', { skip: !MODEL_URI && 'no model' }, async () => {
	await withKind(valveKind(), {}, async (k) => {
		await k.type('sample Pos', '72');
		assert.equal(await k.status(), '72 % open');
		const cmd = await k.field('sample Cmd');
		assert.equal(cmd.value, true);
		await k.b.click(cmd.cx, cmd.cy);
		await sleep(150);
		assert.equal(await k.status(), '72 % shut');
		assert.deepEqual(await k.ops(), []);
		assert.deepEqual(k.exceptions(), []);
	});
});

test('K05 turntable: the model turns while on, holds while off', { skip: !MODEL_URI && 'no model' }, async () => {
	await withKind(valveKind(), {}, async (k) => {
		// Turning is a frame-by-frame animation: on a slow runner (software
		// WebGL in CI) one frame can take longer than any fixed wait, so poll
		// until the angle moves rather than sampling once after a sleep.
		const a = await k.rotation();
		let b = a;
		for (const until = Date.now() + 5000; b === a && Date.now() < until; ) {
			await sleep(100);
			b = await k.rotation();
		}
		assert.ok(a !== null && b !== null && b !== a, `turning: ${a} -> ${b} (5 s)`);
		const btn = await k.button('Turntable');
		assert.ok(btn.active);
		await k.b.click(btn.cx, btn.cy);
		await sleep(200);
		const c = await k.rotation();
		await sleep(400);
		const d = await k.rotation();
		assert.equal(c, d, `held: ${c} -> ${d}`);
		assert.deepEqual(await k.ops(), []);
	});
});

test('K06 missing: a model the host did not find — the panel says so, drives are editable by name', async () => {
	await withKind(valveKind(), { found: false }, async (k) => {
		assert.equal(await k.state(), 'model not found');
		const drives = await k.drives();
		assert.equal(drives[0].mesh, 'Handle');
		assert.equal(drives[0].options.length, 0, 'the mesh is a text field, not a menu');
		const labels = await k.labels();
		assert.ok(labels.some((l) => l.title === 'valve-x'), 'the placeholder box still carries the label');
		await k.type('mesh', 'Stem');
		assert.deepEqual(await k.ops(), [{ type: 'updateDrive', index: 0, drive: { mesh: 'Stem', turn: { axis: 'z', deg: { bind: 'Pos', scale: 0.9 } } } }]);
		assert.deepEqual(k.exceptions(), []);
	});
});

test('K07 pick: hover and click on the model pick a part, "Add drive" starts on it, a chip picks by name, Esc clears', { skip: !MODEL_URI && 'no model' }, async () => {
	await withKind(valveKind(), {}, async (k) => {
		// Hold the turntable so a part stays where partAt says it is.
		const tt = await k.button('Turntable');
		await k.b.click(tt.cx, tt.cy);
		await sleep(200);
		const body = await k.partAt('Body');
		assert.ok(body, 'the Body is on stage');
		await k.b.moveTo(body.x, body.y);
		await sleep(200);
		assert.equal(await k.hover(), 'Body', 'the pointer over the body hovers it');
		assert.equal(await k.picked(), null);
		await k.b.click(body.x, body.y);
		await sleep(250);
		assert.equal(await k.picked(), 'Body');
		assert.match(await k.pickedLine(), /^Body · 0 drives · clear$/);
		assert.deepEqual(await k.ops(), [], 'a pick is webview state, not an op');
		// "Add drive" starts on the picked part.
		const add = await k.button('Add drive');
		assert.ok(add, 'Add drive');
		await k.b.click(add.cx, add.cy);
		await sleep(200);
		const ops = await k.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.equal(ops[0].type, 'addDrive');
		assert.equal(ops[0].drive.mesh, 'Body');
		await k.reset();
		// A chip picks by name; the part's drive row lights.
		const handle = (await k.parts()).find((p) => p.name === 'Handle');
		assert.ok(handle && handle.drives === 1, JSON.stringify(await k.parts()));
		await k.b.click(handle.cx, handle.cy);
		await sleep(200);
		assert.equal(await k.picked(), 'Handle');
		assert.equal(await k.rowPicked(0), true, 'the Handle drive row is marked');
		assert.match(await k.pickedLine(), /^Handle · 1 drive · clear$/);
		// Esc clears; a click on the ground clears too.
		await k.b.pressKey('Escape', { keyCode: 27 });
		await sleep(150);
		assert.equal(await k.picked(), null);
		assert.equal(await k.rowPicked(0), false);
		await k.b.click(body.x, body.y);
		await sleep(200);
		assert.equal(await k.picked(), 'Body');
		const c = await k.b.eval('JSON.stringify(window.__canvas())').then(JSON.parse);
		await k.b.click(c.left + 20, c.top + c.h - 20);
		await sleep(200);
		assert.equal(await k.picked(), null, 'a click on empty ground clears the pick');
		assert.deepEqual(await k.ops(), []);
		assert.deepEqual(k.exceptions(), []);
	});
});

test('K08 ports: a click records where it landed; "Add port here" posts setPort there facing out; a port is a mark and a row; rename and delete', { skip: !MODEL_URI && 'no model' }, async () => {
	const kind = { ...valveKind(), ports: { inlet: { at: [-0.1, 0.05, 0], dir: [-1, 0, 0] } } };
	await withKind(kind, {}, async (k) => {
		const tt = await k.button('Turntable');
		await k.b.click(tt.cx, tt.cy);
		await sleep(200);
		// The file's port is on stage and in the panel.
		const marks = await k.kports();
		assert.deepEqual(marks.map((m) => m.name), ['inlet'], JSON.stringify(marks));
		const rows = await k.portRows();
		assert.equal(rows.length, 1);
		assert.deepEqual(rows[0].at, [-0.1, 0.05, 0]);
		assert.deepEqual(rows[0].dir, [-1, 0, 0]);
		assert.ok(await k.button('Add port here'), 'Add port here');
		assert.equal(await k.hit(), null, 'nothing clicked yet');
		// Click the body: the hit is a point on it, with an outward normal.
		const body = await k.partAt('Body');
		await k.b.click(body.x, body.y);
		await sleep(250);
		assert.equal(await k.picked(), 'Body');
		const hit = await k.hit();
		assert.ok(hit && hit.at.length === 3 && hit.dir.length === 3, JSON.stringify(hit));
		assert.ok(Math.abs(Math.hypot(...hit.dir) - 1) < 0.01, `unit normal: ${hit.dir}`);
		// The panel grew with the pick (the picked line): find the button again.
		const add = await k.button('Add port here');
		await k.b.click(add.cx, add.cy);
		await sleep(200);
		let ops = await k.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual(ops[0], { type: 'setPort', name: 'body', port: { at: hit.at, dir: hit.dir } });
		await k.reset();
		// Rename and delete go through the row.
		await k.type('port name', 'drain');
		assert.deepEqual(await k.ops(), [{ type: 'renamePort', name: 'inlet', to: 'drain' }]);
		await k.reset();
		const del = await k.b.eval(`JSON.stringify((() => { const b = document.querySelector('[data-kind=port-row] [aria-label="remove port"]'); const r = b.getBoundingClientRect(); return { cx: r.left + r.width / 2, cy: r.top + r.height / 2 }; })())`).then(JSON.parse);
		await k.b.click(del.cx, del.cy);
		await sleep(150);
		assert.deepEqual(await k.ops(), [{ type: 'deletePort', name: 'inlet' }]);
		assert.deepEqual(k.exceptions(), []);
	});
});

test('K09 anchors and ramps: a port at top of a part is a mark on it; "Add port at…" posts a box point; the label goes on a part; a tint becomes a ramp', { skip: !MODEL_URI && 'no model' }, async () => {
	const kind = { ...valveKind(), ports: { top: { at: { part: 'Body', anchor: 'top' } }, side: { at: { anchor: 'left' } } } };
	kind.drive.push({ mesh: 'Body', tint: { bind: 'Running', on: 'running' } });
	await withKind(kind, {}, async (k) => {
		const tt = await k.button('Turntable');
		await k.b.click(tt.cx, tt.cy);
		await sleep(200);
		// Both ports resolved to marks: on the body's box, and on the bounds.
		const marks = await k.kports();
		assert.deepEqual(marks.map((m) => m.name).sort(), ['side', 'top'], JSON.stringify(marks));
		const body = await k.partAt('Body');
		const top = marks.find((m) => m.name === 'top');
		assert.ok(top.y < body.y, `the top port sits above the body's centre: ${top.y} < ${body.y}`);
		assert.equal(await k.b.eval(`window.__pointMode('[data-kind=port-row][data-name=top]')`), 'part');
		assert.equal(await k.b.eval(`window.__pointMode('[data-kind=port-row][data-name=side]')`), 'bounds');
		// Pick the body, then "Add port at… bottom of Body".
		await k.b.click(body.x, body.y);
		await sleep(250);
		assert.equal(await k.picked(), 'Body');
		await k.reset();
		assert.ok(await k.b.eval(`window.__select('add port at', 'part:bottom')`));
		await sleep(200);
		assert.deepEqual(await k.ops(), [{ type: 'setPort', name: 'body', port: { at: { part: 'Body', anchor: 'bottom' } } }]);
		await k.reset();
		// The label: default → set → on a part.
		const set = await k.b.eval(`JSON.stringify((() => { const b = [...document.querySelectorAll('aside[aria-label=Kind] button')].find((x) => x.textContent.trim() === 'set'); const r = b.getBoundingClientRect(); return { cx: r.left + r.width / 2, cy: r.top + r.height / 2 }; })())`).then(JSON.parse);
		await k.b.click(set.cx, set.cy);
		await sleep(200);
		assert.deepEqual(await k.ops(), [{ type: 'setField', key: 'labelAt', value: 'top' }]);
		await k.deliver({ ...kind, labelAt: 'top' });
		await k.reset();
		assert.ok(await k.b.eval(`window.__select('label mode', 'part')`));
		await sleep(200);
		assert.deepEqual(await k.ops(), [{ type: 'setField', key: 'labelAt', value: { part: 'Body', anchor: 'top' } }], 'the picked part is the default');
		await k.reset();
		// The tint on the body becomes a ramp: two stops, the member a number.
		await k.b.eval(`(() => { const c = document.querySelector('[data-kind=drive-row][data-index="1"] [aria-label=ramp]'); c.checked = true; c.dispatchEvent(new Event('change', { bubbles: true })); })()`);
		await sleep(200);
		const ops = await k.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual(ops[0], { type: 'updateDrive', index: 1, drive: { mesh: 'Body', tint: { bind: 'Running', ramp: [[0, 'stale'], [100, 'running']] } } });
		const ramped = { ...kind, drive: [kind.drive[0], ops[0].drive] };
		await k.deliver(ramped);
		await k.reset();
		assert.deepEqual(await k.b.eval('JSON.stringify(window.__rampStops(1))').then(JSON.parse), [[0, 'stale'], [100, 'running']]);
		const sample = await k.field('sample Running');
		assert.equal(sample.tag, 'INPUT');
		assert.notEqual(await k.b.eval(`document.querySelector('[aria-label="sample Running"]').type`), 'checkbox', 'a ramp reads its member as a number');
		assert.deepEqual(k.exceptions(), []);
	});
});

test('K10 presets: "Blink on" and "Pulse on" each post ONE addDrive on the picked part, reading the first boolean member; the rows show blink / Hz / ± fields', { skip: !MODEL_URI && 'no model' }, async () => {
	await withKind(valveKind(), {}, async (k) => {
		// Pick a part by its chip, so the presets land on it.
		await k.b.eval(`document.querySelector('[data-kind=part][data-name=Handle]').click()`);
		await sleep(100);
		const blink = await k.button('Blink on');
		assert.ok(blink, 'a Blink preset');
		await k.b.click(blink.cx, blink.cy);
		await sleep(150);
		let ops = await k.ops();
		assert.equal(ops.length, 1, JSON.stringify(ops));
		assert.deepEqual(ops[0], { type: 'addDrive', drive: { mesh: 'Handle', emissive: { bind: 'Cmd', on: 'critical', intensity: 2, blink: 2 } } });
		await k.reset();
		const pulse = await k.button('Pulse on');
		await k.b.click(pulse.cx, pulse.cy);
		await sleep(150);
		ops = await k.ops();
		assert.deepEqual(ops[0], { type: 'addDrive', drive: { mesh: 'Handle', pulse: { bind: 'Cmd', hz: 1, amount: 0.1 } } });
		// Echoed back, the rows carry the clocked fields.
		const kind = valveKind();
		kind.drive = [ops[0].drive, { mesh: 'Handle', emissive: { bind: 'Cmd', on: 'critical', intensity: 2, blink: 2 } }];
		await k.reset();
		await k.deliver(kind);
		await sleep(300);
		const rows = await k.drives();
		assert.equal(rows[0].channel, 'pulse');
		assert.ok(rows[0].fields.includes('hz') && rows[0].fields.includes('amount'), JSON.stringify(rows[0].fields));
		assert.ok(rows[1].fields.includes('blink'), JSON.stringify(rows[1].fields));
		assert.deepEqual(k.exceptions(), []);
	});
});
