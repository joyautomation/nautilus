// Shared helpers and fixture models for the FBD / Ladder / SFC diagram
// gesture suites (diagram.test.mjs and any sibling *.test.mjs). Importing this
// registers no tests. Bundle under test: env DIAGRAM_BUNDLE, else ../media/dist.
import assert from 'node:assert/strict';
import { mkdtempSync, copyFileSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser } from './cdp.mjs';
import { applyThemeJs } from './themes.mjs';
import { recordClips } from './clips.mjs';

export { recordClips, Browser, applyThemeJs };
export const HERE = dirname(fileURLToPath(import.meta.url));
export const BUNDLE = process.env.DIAGRAM_BUNDLE || join(HERE, '../../media/dist');
export const HEADLESS = process.env.HEADED !== '1';
export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export async function open({ state, forwardKeys } = {}) {
	const dir = mkdtempSync(join(tmpdir(), 'diagram-run-'));
	// `state` seeds the webview state (vscode.getState) the bundle reads at
	// mount — a panel reopening with what it saved.
	const html = readFileSync(join(HERE, 'diagram-host.html'), 'utf8').replace(
		'window.__POSTED__ = [];',
		`window.__POSTED__ = []; window.__STATE__ = ${JSON.stringify(state ?? null)};`
	);
	// `forwardKeys` is the preview panel: Ctrl+Z/Y/S post `diagramKey` to the host.
	writeFileSync(join(dir, 'host.html'), forwardKeys ? html.replace('<body>', '<body data-forward-keys="1">') : html);
	copyFileSync(join(BUNDLE, 'fbd-flow.js'), join(dir, 'fbd-flow.js'));
	copyFileSync(join(BUNDLE, 'fbd-flow.css'), join(dir, 'fbd-flow.css'));
	const b = await Browser.launch({ headless: HEADLESS });
	await b.navigate('file://' + join(dir, 'host.html'));
	return b;
}

export async function withPage(fn, opts) {
	const b = await open(opts);
	try {
		await fn(b);
		assert.deepEqual(await b.eval('window.__errors'), [], 'page threw');
	} finally {
		await b.close();
	}
}

export const deliver = async (b, msg) => {
	await b.eval(`window.__deliver(${JSON.stringify(msg)})`);
	await sleep(250);
};

export const posted = (b) => b.eval('window.__POSTED__');

export const reset = (b) => b.eval('window.__reset()');

export const center = (b, sel) => b.eval(`window.__center(${JSON.stringify(sel)})`);

export const text = (b) => b.eval('document.body.innerText');

export async function key(b, k, code, keyCode, modifiers = 0) {
	await b.pressKey(k, { code, keyCode, modifiers });
	await sleep(120);
}

export const del = (b) => key(b, 'Delete', 'Delete', 46);

export const esc = (b) => key(b, 'Escape', 'Escape', 27);

export async function clickAt(b, pt) {
	assert.ok(pt, 'target not rendered');
	await b.click(pt.x, pt.y);
	await sleep(120);
}
/** Click with Control held the way xyflow sees it (it tracks keydown, not
 * the event's modifier bits). */

export async function ctrlClick(b, pt) {
	const base = { key: 'Control', code: 'ControlLeft', windowsVirtualKeyCode: 17, modifiers: 2 };
	await b.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', ...base });
	await b.click(pt.x, pt.y, { modifiers: 2 });
	await b.send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
	await sleep(120);
}

// `naut fbd graph` of a coil fed by AND(A, B) plus three comment notes.
export const FBD = {
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
/** Locate by the testability attributes (data-kind + data-id), not CSS/DOM order. */

export const byId = (kind, id) => `[data-kind="${kind}"][data-id="${id}"]`;

export const node = (id) => `.svelte-flow__node[data-id="${id}"]`;

export const fbdOps = async (b) => (await posted(b)).filter((m) => m.type === 'edit').map((m) => m.op);

// A point ON an edge's path (its bbox center can miss a bent wire).
export const edgePoint = (b, to, toPin) =>
	b.eval(`(() => {
		const g = [...document.querySelectorAll('.svelte-flow__edge')].find((el) => (el.getAttribute('data-id') ?? '').includes('|${to}|${toPin}|'));
		const path = g && (g.querySelector('path.svelte-flow__edge-interaction') ?? g.querySelector('path'));
		if (!path) return null;
		const p = path.getPointAtLength(path.getTotalLength() * 0.6);
		const m = path.getScreenCTM();
		return { x: p.x * m.a + p.y * m.c + m.e, y: p.x * m.b + p.y * m.d + m.f };
	})()`);

// ── Ladder ──────────────────────────────────────────────────────────────
export const LD = {
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

export const ldOps = async (b) => (await posted(b)).filter((m) => m.type === 'ldEdit').map((m) => m.op);

export const paletteBtn = (b, label) =>
	b.eval(`(() => { const el = [...document.querySelectorAll('.palette button')].find((x) => x.textContent.trim() === ${JSON.stringify(label)}); if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);

// ── SFC ─────────────────────────────────────────────────────────────────
export const SFC = {
	name: 'Seq',
	steps: [
		{ id: 'st:Idle', name: 'Idle', initial: true, line: 3, endLine: 4 },
		{ id: 'st:Run', name: 'Run', initial: false, line: 5, endLine: 6 },
		{ id: 'st:Spare', name: 'Spare', initial: false, line: 9, endLine: 10 }
	],
	trans: [{ id: 'tr:7', from: ['Idle'], to: ['Run'], cond: 'go', kind: 'normal', line: 7, endLine: 8 }]
};

export const sfcOps = async (b) => (await posted(b)).filter((m) => m.type === 'sfcEdit').map((m) => m.op);

// ── clipboard, select-all, shortcut help (editor parity) ──────────────────
// Headless Chrome denies the system clipboard, so these exercise the
// in-webview fallback — the path a paste must never depend on the other.
export async function ctrlKey(b, k) {
	await key(b, k, 'Key' + k.toUpperCase(), k.toUpperCase().charCodeAt(0), 2);
	await sleep(500); // readClip gives the system clipboard up to 400 ms
}

export const FBD_SRC = 'PROGRAM Main\nFBD\n  Y := AND(A, B)\nEND_FBD\nEND_PROGRAM\n';

export const sfcStepPt = (b, name) =>
	b.eval(`(() => { const el = [...document.querySelectorAll('.step .stepname')].find((x) => x.textContent === ${JSON.stringify(name)}); if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);

// ── restore from saved webview state ────────────────────────────────────
// "Developer: Reload Webviews" / Reload Window / a hidden tab coming back:
// the bundle mounts with vscode.getState() already holding the last model
// message and re-shows it BEFORE the host replays. A throw on that path
// aborts the mount — no `ready`, so the host never replays and the panel
// stays blank for good (the FBD editor did exactly that: show() assigned
// state declared further down the component).
export const ready = async (b) => (await posted(b)).filter((m) => m.type === 'ready');

export async function restores(saved, check, zoom) {
	await withPage(
		async (b) => {
			await sleep(250);
			assert.deepEqual(await ready(b), [{ type: 'ready' }], 'mount must still say ready');
			await check(b);
		},
		{ state: { msg: saved, ...(zoom ? { zoom } : {}) } }
	);
}

// ── zoom / pan / fit (Ladder + SFC) ─────────────────────────────────────
// ZoomPane draws the SVGs at width/height × zoom over an unscaled viewBox,
// so every hit-test stays in screen space; these pin that the gestures
// that do their own coordinate math (SFC drag / connect) divide the zoom
// back out, and that Ladder's elementFromPoint drops still land.
export async function wheel(b, x, y, deltaY, modifiers = 2) {
	await b.moveTo(x, y);
	await b.send('Input.dispatchMouseEvent', { type: 'mouseWheel', x, y, deltaX: 0, deltaY, modifiers });
	await sleep(120);
}

export const zoomPct = (b) => b.eval(`document.querySelector('.zpct')?.textContent`);

export const zoomBtn = (b, label) => center(b, `.zctl button[aria-label="${label}"]`);

export const rect = (b, sel, i = 0) =>
	b.eval(`(() => { const el = document.querySelectorAll(${JSON.stringify(sel)})[${i}]; if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height, cx: r.left + r.width / 2, cy: r.top + r.height / 2 }; })()`);

export const stepCenter = (b, name) =>
	b.eval(`(() => { const el = [...document.querySelectorAll('.step')].find((g) => g.querySelector('.stepname')?.textContent === ${JSON.stringify(name)}); const r = el.querySelector('.box').getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2, w: r.width }; })()`);

// Enough rungs to scroll: a zoom can only hold its anchor still when the
// pane has room to scroll the magnified content under it.
export const LD_LONG = {
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

// A chart tall enough to overflow the 900px harness window.
export const TALL = {
	name: 'Long',
	steps: Array.from({ length: 12 }, (_, i) => ({ id: `st:S${i}`, name: `S${i}`, initial: i === 0, line: 3 + 2 * i, endLine: 4 + 2 * i })),
	trans: Array.from({ length: 11 }, (_, i) => ({ id: `tr:${40 + i}`, from: [`S${i}`], to: [`S${i + 1}`], cond: 'go', kind: 'normal', line: 40 + i, endLine: 40 + i }))
};

// ── theme ───────────────────────────────────────────────────────────────
export const css = (b, sel, prop) => b.eval(`(() => { const el = document.querySelector(${JSON.stringify(sel)}); return el && getComputedStyle(el).getPropertyValue(${JSON.stringify(prop)}).trim(); })()`);

// ── FBD palette: the function-block picker ──────────────────────────────────
// A diagram with one PID instance already on it, and the catalog `naut fbd
// graph` sends (PID with its pins, a project block).
export const PID_PINS = [
	...['AUTO', 'PV', 'SP'].map((name) => ({ name, type: name === 'AUTO' ? 'BOOL' : 'REAL', dir: 'in' })),
	...['CV', 'SAT_HI'].map((name) => ({ name, type: name === 'CV' ? 'REAL' : 'BOOL', dir: 'out' }))
];

export const FBD_PID = {
	...FBD,
	nodes: [
		...FBD.nodes,
		{ id: 'f:pid1', kind: 'fb', label: 'pid1', type: 'PID', inputs: ['AUTO', 'PV', 'SP'], outputs: ['CV', 'SAT_HI'], layer: 1, line: 12 }
	],
	fbTypes: [
		{ name: 'TON', detail: 'on-delay timer', prefix: 't', pins: [{ name: 'IN', type: 'BOOL', dir: 'in' }, { name: 'PT', type: 'TIME', dir: 'in' }, { name: 'Q', type: 'BOOL', dir: 'out' }], args: 'IN := _, PT := _' },
		{ name: 'PID', detail: 'closed-loop control', prefix: 'pid', pins: PID_PINS, args: 'AUTO := _, PV := _, SP := _' },
		{ name: 'Starter', user: true, prefix: 's', pins: [{ name: 'Req', type: 'BOOL', dir: 'in' }, { name: 'Run', type: 'BOOL', dir: 'out' }], args: 'Req := _' }
	]
};

export const btnByText = (b, sel, label) =>
	b.eval(`(() => { const el = [...document.querySelectorAll(${JSON.stringify(sel)})].find((x) => (x.querySelector('span')?.textContent ?? x.textContent).trim() === ${JSON.stringify(label)}); if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);

export const typeText = async (b, s) => {
	for (const ch of s) await b.send('Input.insertText', { text: ch });
	await sleep(80);
};
