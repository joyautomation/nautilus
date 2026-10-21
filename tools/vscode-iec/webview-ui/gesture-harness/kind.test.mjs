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
	await withKind(valveKind(), {}, async (k) => {
		await k.type('type', 'ControlValve');
		assert.deepEqual(await k.ops(), [{ type: 'setField', key: 'type', value: 'ControlValve' }]);
		await k.reset();
		await k.type('status', '');
		assert.deepEqual(await k.ops(), [{ type: 'setField', key: 'status', value: null }]);
		await k.reset();
		await k.type('labelAt y', '0.3');
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
		const a = await k.rotation();
		await sleep(400);
		const b = await k.rotation();
		assert.ok(a !== null && b !== null && b !== a, `turning: ${a} -> ${b}`);
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
