// The gesture harness: loads a PRODUCTION mimic-editor bundle in headless
// Chrome, performs the host side of the webview message protocol
// (src/mimicEditor.ts) with a test doc, and exposes high-level gesture helpers
// (enter pipe mode, click a named port, hover, press Enter, drag a vertex)
// that drive REAL pointer/keyboard input. Every op the webview posts back is
// captured on window.__POSTED__.
//
// This outlives any single bug: it's the missing browser-gesture layer for the
// mimic webview, whose pure-logic tests (pipeDraft.test.ts etc.) can't see
// focus, pointer capture, hit-testing or preview-vs-commit geometry.

import { Browser } from './cdp.mjs';
import { mkdtempSync, copyFileSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** A two-Tank test doc modeled on the heated-tank demo: builtin Tank ports
 * (top/left/right/bottom), well separated so T1.right and T2.left form a
 * clean facing port pair. */
export function twoTankDoc() {
	return {
		name: 'harness',
		canvas: { width: 900, height: 500 },
		equipment: [
			{ id: 'T1', component: 'Tank', x: 120, y: 180, width: 120, label: 'T1' },
			{ id: 'T2', component: 'Tank', x: 600, y: 180, width: 120, label: 'T2' }
		],
		pipes: [],
		labels: []
	};
}

export class Editor {
	constructor(browser) {
		this.b = browser;
	}

	/** Open the bundle on `doc`. `state` seeds vscode.getState() before
	 * mount (a panel restored by Reload Webviews / Reload Window);
	 * `mode: 'component'` mounts the *.component.json editor instead, with
	 * `doc` = { component, ports }. */
	static async open(bundleDir, doc, { headless = true, state, mode = 'mimic' } = {}) {
		// Stage host.html next to the chosen bundle so ./mimic-editor.js
		// resolves to exactly the build under test.
		const runDir = mkdtempSync(join(tmpdir(), 'mimic-run-'));
		let html = readFileSync(join(HERE, 'host.html'), 'utf8');
		if (state !== undefined) html = html.replace('window.__POSTED__ = [];', `window.__POSTED__ = []; window.__STATE__ = ${JSON.stringify(state)};`);
		if (mode === 'component') html = html.replace('data-mimic-mode="mimic"', 'data-mimic-mode="component"');
		writeFileSync(join(runDir, 'host.html'), html);
		copyFileSync(join(bundleDir, 'mimic-editor.js'), join(runDir, 'mimic-editor.js'));
		copyFileSync(join(bundleDir, 'mimic-editor.css'), join(runDir, 'mimic-editor.css'));

		const b = await Browser.launch({ headless });
		const ed = new Editor(b);
		await b.navigate('file://' + join(runDir, 'host.html'));

		// Wait for the bundle to mount + announce ready.
		if (mode === 'component') {
			for (let i = 0; i < 100; i++) {
				if (await b.eval(`window.__posted().some((m) => m && m.type === 'componentReady')`)) break;
				await sleep(50);
			}
			await ed.deliver({ type: 'componentDoc', ...doc, title: 'harness.component.json' });
			await sleep(200);
			return ed;
		}
		for (let i = 0; i < 100; i++) {
			if (await b.eval('window.__ready && window.__ready()')) break;
			await sleep(50);
		}
		const posted = await b.eval('JSON.stringify(window.__posted().map((m)=>m&&m.type))');
		if (!JSON.parse(posted).includes('mimicReady')) {
			// Not fatal (a preloaded __MODEL__ path also works) but worth knowing.
		}
		// Host side of the protocol: doc, manifest (builtin ports need none, but
		// send an empty one so ed.manifest isn't null), config, tags.
		await ed.deliver({ type: 'mimicDoc', doc, title: 'harness.mimic.json' });
		await ed.deliver({ type: 'mimicManifest', components: {}, customComponents: [] });
		await ed.deliver({ type: 'mimicConfig', snapToGrid: true });
		await ed.deliver({ type: 'mimicTags', tags: null });

		// Wait until both equipment boxes have rendered.
		for (let i = 0; i < 100; i++) {
			if ((await b.eval('window.__eqRects().length')) >= 2) break;
			await sleep(50);
		}
		await sleep(80); // let ResizeObserver settle scale + ports project
		return ed;
	}

	deliver(msg) {
		return this.b.eval(`window.__deliver(${JSON.stringify(msg)}), true`);
	}
	resetOps() {
		return this.b.eval('window.__reset(), true');
	}
	ops() {
		return this.b.eval('JSON.stringify(window.__ops())').then(JSON.parse);
	}
	/** `manifestOp` messages (postManifestOp) — the channel ports-edit's
	 * default "shared" target commits through, distinct from `mimicOp`
	 * (`ops()`, above). */
	manifestOps() {
		return this.b
			.eval("JSON.stringify(window.__posted().filter((m) => m && m.type === 'manifestOp').map((m) => m.op))")
			.then(JSON.parse);
	}
	ports() {
		return this.b.eval('JSON.stringify(window.__ports())').then(JSON.parse);
	}
	/** Ports-EDIT-mode dots (.porthandle) — only rendered while ed.portsEdit
	 * targets an equipment (see enterPortsMode). */
	portHandles() {
		return this.b.eval('JSON.stringify(window.__portHandles())').then(JSON.parse);
	}
	eqRects() {
		return this.b.eval('JSON.stringify(window.__eqRects())').then(JSON.parse);
	}
	tools() {
		return this.b.eval('JSON.stringify(window.__tools())').then(JSON.parse);
	}
	draft() {
		return this.b.eval('JSON.stringify(window.__draft())').then(JSON.parse);
	}
	pipePaths() {
		return this.b.eval('JSON.stringify(window.__pipePaths())').then(JSON.parse);
	}
	vtx() {
		return this.b.eval('JSON.stringify(window.__vtx())').then(JSON.parse);
	}
	/** Read-only `ed.selection` KIND summary (see host.html's __selection) —
	 * BUG 1 coverage reads `.nodesCount` after each shift-click to confirm the
	 * multi-selection actually grew/shrank as expected, independent of
	 * whatever the pipe geometry did. */
	selection() {
		return this.b.eval('JSON.stringify(window.__selection())').then(JSON.parse);
	}
	console() {
		return this.b.consoleLines.slice();
	}
	/** Rendered geometry + computed PAINT style of every element matching
	 * `selector`, in DOM order — the layer underneath which a handle can go
	 * quietly invisible (or lose its selected-state visual) even though its
	 * CLASS list — the only thing `ed.selection()`/`ed.vtx()` and every
	 * ops()-based assertion in this suite check — is exactly right. Reads
	 * `getBoundingClientRect` (nonzero-area proof something actually
	 * occupies pixels) and `getComputedStyle` (the CASCADE's final answer,
	 * not the source CSS — this is what catches a `.sel` rule that stopped
	 * matching or got shadowed after a layer move) the same way a user's
	 * eyes would, rather than trusting the class attribute alone. */
	async visualState(selector) {
		return this.b
			.eval(
				`JSON.stringify(Array.from(document.querySelectorAll(${JSON.stringify(selector)})).map((el) => {
					const cs = getComputedStyle(el);
					const r = el.getBoundingClientRect();
					return {
						classes: el.getAttribute('class') || '',
						fill: cs.fill,
						stroke: cs.stroke,
						strokeWidth: cs.strokeWidth,
						opacity: cs.opacity,
						visibility: cs.visibility,
						display: cs.display,
						area: r.width * r.height,
					};
				}))`
			)
			.then(JSON.parse);
	}
	/** Map a canvas coordinate to a viewport [x, y] for driving input. */
	toVp(cx, cy) {
		return this.b.eval(`JSON.stringify(window.__toVp(${cx}, ${cy}))`).then(JSON.parse);
	}

	/** Re-deliver a doc (e.g. after an op, to reflect the applied change the
	 * way the host's postDoc round-trip would). */
	async setDoc(doc) {
		await this.deliver({ type: 'mimicDoc', doc, title: 'harness.mimic.json' });
		await sleep(60);
	}

	// ── gesture layer ─────────────────────────────────────────────────────────
	async enterPipeMode() {
		const tools = await this.tools();
		const pipe = tools.find((t) => t.label.includes('Pipe'));
		await this.b.click(pipe.cx, pipe.cy);
		await sleep(40);
	}
	async selectMode() {
		const tools = await this.tools();
		const sel = tools.find((t) => t.label === 'Select');
		await this.b.click(sel.cx, sel.cy);
		await sleep(40);
	}

	/** Viewport center of equipment `eqIndex`'s port named `name` — the .port
	 * dot nearest that equipment's box (disambiguates two boxes' same-named
	 * ports). Requires pipe mode (ports only render then). */
	async portXY(eqIndex, name) {
		const [ports, rects] = await Promise.all([this.ports(), this.eqRects()]);
		const box = rects[eqIndex];
		const bx = box.cx;
		const cands = ports.filter((p) => p.name === name);
		if (!cands.length) throw new Error(`no port named ${name} rendered`);
		cands.sort((a, b) => Math.abs(a.cx - bx) - Math.abs(b.cx - bx));
		return cands[0];
	}

	async clickPort(eqIndex, name) {
		const p = await this.portXY(eqIndex, name);
		await this.b.click(p.cx, p.cy);
		await sleep(40);
	}
	async clickCanvas(vx, vy) {
		await this.b.click(vx, vy);
		await sleep(40);
	}
	async hoverPort(eqIndex, name) {
		const p = await this.portXY(eqIndex, name);
		await this.b.moveTo(p.cx, p.cy);
		await sleep(40);
	}
	async hoverAt(vx, vy) {
		await this.b.moveTo(vx, vy);
		await sleep(40);
	}
	async pressEnter() {
		await this.b.pressEnter();
		await sleep(60);
	}
	/** Delete (not Backspace — EditorCanvas treats them identically) — the
	 * key gesture for BUG 1's node multi-select removal. */
	async pressDelete() {
		await this.b.pressKey('Delete', { code: 'Delete', keyCode: 46 });
		await sleep(60);
	}

	/** Click equipment `eqIndex`'s box center (select mode) — selects it, the
	 * precondition for enterPortsMode ('p' only acts on an equipment
	 * selection). */
	async selectEquipment(eqIndex) {
		await this.selectMode();
		const rects = await this.eqRects();
		const r = rects[eqIndex];
		await this.b.click(r.cx, r.cy);
		await sleep(40);
	}

	/** Select equipment `eqIndex` then toggle ports-edit mode on it ('p') —
	 * mirrors the user gesture (select, press p) exactly. */
	async enterPortsMode(eqIndex) {
		await this.selectEquipment(eqIndex);
		await this.b.pressKey('p', { code: 'KeyP', keyCode: 80 });
		await sleep(40);
	}

	/** Click the ports-edit dot named `name` on the currently-portsEdit
	 * equipment — selects it (portsSelected) WITHOUT moving it (a click has
	 * no intervening pointermove, so the drag gesture's `moved` flag never
	 * flips true).
	 *
	 * A dead-center click, same as a real user's — a port sitting exactly on
	 * the equipment box's edge (every builtin port) used to be HALF covered
	 * by the equipment `.eq` div, which painted AFTER (so won hit-testing
	 * over) the ports-edit dots; PORT-DOT Z-ORDER moved the dots into their
	 * own layer painted after `.eq`, the same fix pattern BUG 2 used for
	 * pipe anchors, so this no longer needs to nudge off-center to land on
	 * the dot instead of the box underneath it. */
	async clickPortHandle(name) {
		const handles = await this.portHandles();
		const h = handles.find((p) => p.name === name);
		if (!h) throw new Error(`no ports-edit dot named ${name} rendered`);
		await this.b.click(h.cx, h.cy);
		await sleep(40);
	}

	/** Press an arrow key — `dir` is 'left'/'right'/'up'/'down' — optionally
	 * with Shift (the grid-step modifier every EditorCanvas nudge honors). */
	async pressArrow(dir, { shift = false } = {}) {
		const KEY = { left: 'ArrowLeft', right: 'ArrowRight', up: 'ArrowUp', down: 'ArrowDown' };
		const CODE = { left: 37, right: 39, up: 38, down: 40 };
		const key = KEY[dir];
		await this.b.pressKey(key, { code: key, keyCode: CODE[dir], modifiers: shift ? 8 : 0 });
		await sleep(40);
	}

	// ── select-mode drag primitives (bug #2 coverage) ─────────────────────────
	/** Press at `start`, move through `vias`, capture the DOM at the LAST via
	 * (mid-drag, before release), release at the final via. Returns the
	 * mid-drag capture: rendered pipe paths + vertex handles. */
	async dragAndCapture(start, vias) {
		await this.b.mouseDown(start[0], start[1]);
		await sleep(30);
		let cap = null;
		for (let i = 0; i < vias.length; i++) {
			await this.b.mouseMove(vias[i][0], vias[i][1]);
			await sleep(30);
			if (i === vias.length - 1) cap = { pipePaths: await this.pipePaths(), vtx: await this.vtx() };
		}
		const end = vias[vias.length - 1];
		await this.b.mouseUp(end[0], end[1]);
		await sleep(60);
		return cap;
	}

	/** Click a point (select mode) on the pipe stroke to select it — used to
	 * make vertex handles appear before an interior-vertex drag. */
	async clickPoint(vx, vy) {
		await this.b.click(vx, vy);
		await sleep(40);
	}

	/** Shift-click a vertex handle — the multi-select gesture (vtxDown's
	 * shift branch, EditorCanvas.svelte). `h` is one of `ed.vtx()`'s handles. */
	async shiftClickVtx(h) {
		await this.b.click(h.cx, h.cy, { modifiers: 8 });
		await sleep(40);
	}

	/** Two shift-clicks at the SAME vertex handle in quick succession — the
	 * real mis-click a user makes re-clicking a handle (to confirm it's
	 * picked, or just imprecision) that Chrome synthesizes into a native
	 * `dblclick` on the second one. BUG 1 coverage: this must behave as two
	 * ordinary toggles (net: unchanged if it started unselected, since
	 * add-then-remove cancels out) and must NEVER mutate the pipe's points —
	 * the vertex's own `ondblclick` handler (`vtxDelete`) is a SEPARATE
	 * gesture (double-click WITHOUT shift removes a point outright) that a
	 * held Shift must suppress.
	 *
	 * Deliberately does NOT go through `Browser.click()` for the second
	 * press/release: that helper re-dispatches a `mouseMoved` first (to
	 * settle hover state like a real cursor arriving), and an intervening
	 * `mouseMoved` — even at the SAME point — is enough to make Chrome NOT
	 * coalesce the second click into a native `dblclick` at all, silently
	 * turning this into two harmless independent single clicks and making
	 * the regression this covers un-reproducible. A real double-click has no
	 * such synthetic move between its two press/release pairs, so this
	 * matches that shape exactly: one `moveTo`, then two full click cycles. */
	async shiftDoubleClickVtx(h) {
		const { cx, cy } = h;
		await this.b.moveTo(cx, cy);
		await this.b.send('Input.dispatchMouseEvent', { type: 'mousePressed', x: cx, y: cy, button: 'left', buttons: 1, clickCount: 1, modifiers: 8 });
		await this.b.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: cx, y: cy, button: 'left', buttons: 0, clickCount: 1, modifiers: 8 });
		// NO delay before the second press: even a small (~20ms) sleep here
		// is enough for Chrome to NOT coalesce it into a real `dblclick` —
		// see the comment above; this must mirror repro5's exact working
		// timing, not just its event shape.
		await this.b.send('Input.dispatchMouseEvent', { type: 'mousePressed', x: cx, y: cy, button: 'left', buttons: 1, clickCount: 2, modifiers: 8 });
		await this.b.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: cx, y: cy, button: 'left', buttons: 0, clickCount: 2, modifiers: 8 });
		await sleep(40);
	}

	close() {
		return this.b.close();
	}
}

/** Minimal host-side reducer for the pipe ops the drag gestures emit, so the
 * harness can reflect a committed op back as an updated doc and read the
 * "what materializes on release" rendered geometry — the real reducer lives in
 * the extension host (mimicOps.ts) and isn't loaded in the webview harness. */
export function applyOpToDoc(doc, op) {
	const d = JSON.parse(JSON.stringify(doc));
	const apply = (o) => {
		if (o.type === 'addPipe') {
			d.pipes = d.pipes ?? [];
			const pipe = { id: o.id ?? 'p' + (d.pipes.length + 1), points: o.points };
			if (o.from) pipe.from = o.from;
			if (o.to) pipe.to = o.to;
			if (o.routing) pipe.routing = o.routing;
			d.pipes.push(pipe);
		} else if (o.type === 'updatePipe') {
			const p = d.pipes.find((p) => p.id === o.id);
			if (p) {
				for (const [k, v] of Object.entries(o.patch)) {
					if (v === null) delete p[k];
					else p[k] = v;
				}
			}
		} else if (o.type === 'setPipePoints') {
			const p = d.pipes.find((p) => p.id === o.id);
			if (p) p.points = o.points;
		} else if (o.type === 'moveEquipment') {
			const e = d.equipment.find((e) => e.id === o.id);
			if (e) {
				e.x = o.x;
				e.y = o.y;
			}
		} else if (o.type === 'batch') {
			o.ops.forEach(apply);
		}
	};
	apply(op);
	return d;
}
