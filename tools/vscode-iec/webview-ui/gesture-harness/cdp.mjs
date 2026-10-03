// Minimal Chrome DevTools Protocol client over the WebSocket transport.
// Uses Node 22's built-in global `WebSocket` and `fetch` — no npm deps — so
// the gesture harness stays a zero-install `npm run test:gestures`.
//
// Only the slice of CDP the harness needs: launch headless Chrome, open one
// page target, dispatch REAL pointer/keyboard input (Input.dispatch*), and
// evaluate JS in the page. Real input (not synthetic DOM dispatch) is the
// whole point — focus, pointer capture and z-order hit-testing behave exactly
// as they do for a user, which is where webview gesture bugs actually live.

import { spawn, execSync } from 'node:child_process';
import { mkdtempSync, readFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const CHROME_CANDIDATES = ['google-chrome-stable', 'google-chrome', 'chromium', 'chromium-browser'];

function findChrome() {
	for (const c of CHROME_CANDIDATES) {
		try {
			const p = execSync(`which ${c}`, { stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
			if (p) return p;
		} catch {
			/* not this one */
		}
	}
	throw new Error('No Chrome/Chromium found on PATH (tried: ' + CHROME_CANDIDATES.join(', ') + ')');
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export class Browser {
	constructor(proc, userDataDir, ws) {
		this.proc = proc;
		this.userDataDir = userDataDir;
		this.ws = ws;
		this._id = 0;
		this._pending = new Map();
		this._sessionId = undefined;
		this.consoleLines = [];
		ws.addEventListener('message', (ev) => this._onMessage(ev));
	}

	static async launch({ width = 1400, height = 900, headless = true } = {}) {
		const chrome = findChrome();
		const userDataDir = mkdtempSync(join(tmpdir(), 'mimic-harness-'));
		const args = [
			headless ? '--headless=new' : '--headless=new',
			'--remote-debugging-port=0',
			`--user-data-dir=${userDataDir}`,
			'--no-first-run',
			'--no-default-browser-check',
			'--disable-gpu',
			'--disable-dev-shm-usage',
			// Off by default; GitHub's ubuntu-24.04 runners refuse Chrome's
			// sandbox (AppArmor blocks unprivileged user namespaces), so CI sets this.
			...(process.env.GESTURE_NO_SANDBOX ? ['--no-sandbox'] : []),
			`--window-size=${width},${height}`,
			'about:blank'
		];
		const proc = spawn(chrome, args, { stdio: ['ignore', 'ignore', 'pipe'] });
		// Chrome writes the chosen debugging port to DevToolsActivePort.
		const portFile = join(userDataDir, 'DevToolsActivePort');
		let port = null;
		for (let i = 0; i < 100; i++) {
			if (existsSync(portFile)) {
				const txt = readFileSync(portFile, 'utf8').split('\n')[0].trim();
				if (txt) {
					port = txt;
					break;
				}
			}
			await sleep(50);
		}
		if (!port) {
			proc.kill('SIGKILL');
			throw new Error('Chrome never reported a DevTools port');
		}
		// Find the page target's WebSocket URL.
		let wsUrl = null;
		for (let i = 0; i < 40; i++) {
			try {
				const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
				const page = list.find((t) => t.type === 'page');
				if (page?.webSocketDebuggerUrl) {
					wsUrl = page.webSocketDebuggerUrl;
					break;
				}
			} catch {
				/* not up yet */
			}
			await sleep(50);
		}
		if (!wsUrl) {
			proc.kill('SIGKILL');
			throw new Error('No page target exposed by Chrome');
		}
		const ws = new WebSocket(wsUrl);
		await new Promise((res, rej) => {
			ws.addEventListener('open', res, { once: true });
			ws.addEventListener('error', rej, { once: true });
		});
		const b = new Browser(proc, userDataDir, ws);
		await b.send('Page.enable');
		await b.send('Runtime.enable');
		await b.send('Log.enable');
		return b;
	}

	_onMessage(ev) {
		const msg = JSON.parse(ev.data);
		if (msg.id !== undefined && this._pending.has(msg.id)) {
			const { resolve, reject } = this._pending.get(msg.id);
			this._pending.delete(msg.id);
			if (msg.error) reject(new Error(msg.method + ': ' + JSON.stringify(msg.error)));
			else resolve(msg.result);
			return;
		}
		// Events: capture console output for debugging instrumented builds.
		if (msg.method === 'Runtime.consoleAPICalled') {
			const text = (msg.params.args || []).map((a) => a.value ?? a.description ?? '').join(' ');
			this.consoleLines.push(`[${msg.params.type}] ${text}`);
		} else if (msg.method === 'Log.entryAdded') {
			this.consoleLines.push(`[log:${msg.params.entry.level}] ${msg.params.entry.text}`);
		} else if (msg.method === 'Runtime.exceptionThrown') {
			const d = msg.params.exceptionDetails;
			this.consoleLines.push(`[exception] ${d.exception?.description ?? d.text}`);
		}
	}

	send(method, params = {}) {
		const id = ++this._id;
		return new Promise((resolve, reject) => {
			this._pending.set(id, { resolve, reject });
			this.ws.send(JSON.stringify({ id, method, params }));
		});
	}

	async navigate(url) {
		const loaded = new Promise((res) => {
			const onMsg = (ev) => {
				const m = JSON.parse(ev.data);
				if (m.method === 'Page.loadEventFired') {
					this.ws.removeEventListener('message', onMsg);
					res();
				}
			};
			this.ws.addEventListener('message', onMsg);
		});
		await this.send('Page.navigate', { url });
		await loaded;
	}

	/** Evaluate an expression in the page and return its (JSON) value. */
	async eval(expression) {
		const r = await this.send('Runtime.evaluate', {
			expression,
			returnByValue: true,
			awaitPromise: true
		});
		if (r.exceptionDetails) {
			throw new Error('page eval: ' + (r.exceptionDetails.exception?.description ?? r.exceptionDetails.text));
		}
		return r.result.value;
	}

	// ── real input ──────────────────────────────────────────────────────────
	async moveTo(x, y) {
		await this.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y, button: 'none', buttons: 0 });
	}
	// `modifiers` is the same Input-domain bitmask pressKey uses (Shift=8) —
	// a real shift-click (vertex multi-select) needs it on the press AND
	// release, same as a real held key would be down for both. `clickCount`
	// lets a caller reproduce a REAL double-click (two clicks at the same
	// spot close in time) — Chrome's own click-merging decides whether a
	// same-position second click actually synthesizes a `dblclick` DOM event
	// on the target, regardless of this value, so it's just what a real
	// mouse driver would report at that point in the sequence, not something
	// that forces a dblclick into being.
	async click(x, y, { modifiers = 0, clickCount = 1 } = {}) {
		// Move first (so hover state settles exactly as it does for a user),
		// then a real press/release — Chrome synthesizes the matching
		// pointerdown/up + click (and, for clickCount 2 at the same spot as
		// a recent click, `dblclick`), honoring focus and pointer capture.
		await this.moveTo(x, y);
		await this.send('Input.dispatchMouseEvent', {
			type: 'mousePressed', x, y, button: 'left', buttons: 1, clickCount, modifiers
		});
		await this.send('Input.dispatchMouseEvent', {
			type: 'mouseReleased', x, y, button: 'left', buttons: 0, clickCount, modifiers
		});
	}
	// Decomposed press/move/release so a drag can be observed MID-flight
	// (capture the DOM between move and release).
	async mouseDown(x, y) {
		await this.moveTo(x, y);
		await this.send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', buttons: 1, clickCount: 1 });
	}
	async mouseMove(x, y, { buttons = 1 } = {}) {
		await this.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y, button: 'none', buttons });
	}
	async mouseUp(x, y) {
		await this.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', buttons: 0, clickCount: 1 });
	}
	async dblclick(x, y) {
		await this.moveTo(x, y);
		for (let i = 1; i <= 2; i++) {
			await this.send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', buttons: 1, clickCount: i });
			await this.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', buttons: 0, clickCount: i });
		}
	}
	// `modifiers` is CDP's Input domain bitmask: Alt=1, Ctrl=2, Meta=4, Shift=8
	// — used for Shift-arrow (grid-step nudge) regression coverage.
	async pressKey(key, { code = key, keyCode = 0, modifiers = 0 } = {}) {
		const base = { key, code, windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode, modifiers };
		await this.send('Input.dispatchKeyEvent', { type: 'keyDown', ...base });
		await this.send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
	}
	pressEnter() {
		return this.pressKey('Enter', { code: 'Enter', keyCode: 13 });
	}

	async close() {
		try {
			this.ws.close();
		} catch {
			/* ignore */
		}
		try {
			this.proc.kill('SIGKILL');
		} catch {
			/* ignore */
		}
		try {
			rmSync(this.userDataDir, { recursive: true, force: true });
		} catch {
			/* ignore */
		}
	}
}
