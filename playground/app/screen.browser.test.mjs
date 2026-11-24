// The screen builder is a second wasm module (naut-hmi.wasm, cmd/naut-wasm-hmi),
// fetched only when a screen is first asked for. In a real (headless) Chrome
// over the built playground (run after scripts/build.sh):
//
//     node --test playground/app/screen.browser.test.mjs
//
//  1. a project with a prebuilt screen (lift-station) opens and runs without
//     ever fetching the builder or its packages;
//  2. a project whose screen the browser builds (demo: a mimic, no server.hmi)
//     fetches the builder when its screen is opened, and the screen comes
//     back as HTML, while the engine's worker keeps answering during the build;
//  3. with the builder's download blocked, the screen says so, and the
//     controller (and its dashboard) keeps running.
//
// Chrome comes from the gesture harness's client (no npm dependencies);
// GESTURE_NO_SANDBOX=1 where the sandbox is refused (GitHub's runners).
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { join, extname, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser } from '../../tools/vscode-iec/webview-ui/gesture-harness/cdp.mjs';

const dist = fileURLToPath(new URL('../dist/', import.meta.url));
const types = {
	'.html': 'text/html',
	'.js': 'text/javascript',
	'.mjs': 'text/javascript',
	'.json': 'application/json',
	'.wasm': 'application/wasm',
	'.css': 'text/css',
	'.svg': 'image/svg+xml',
	'.png': 'image/png',
	'.woff': 'font/woff',
	'.woff2': 'font/woff2',
	'.ttf': 'font/ttf'
};

let server, base;
before(async () => {
	server = createServer(async (req, res) => {
		const path = decodeURIComponent(new URL(req.url, 'http://x').pathname);
		const file = normalize(join(dist, path.endsWith('/') ? path + 'index.html' : path));
		if (!file.startsWith(dist)) return res.writeHead(403).end();
		try {
			const body = await readFile(file);
			res.writeHead(200, { 'content-type': types[extname(file)] ?? 'application/octet-stream' });
			res.end(body);
		} catch {
			res.writeHead(404).end();
		}
	});
	await new Promise((r) => server.listen(0, '127.0.0.1', r));
	base = `http://127.0.0.1:${server.address().port}`;
});
after(() => server?.close());

/** Every request any page or worker makes (the engine and the builder run in
 * workers), by URL; `block` lists URL patterns whose requests fail. */
async function watch(b, block = []) {
	const seen = [];
	let id = 2e6;
	const raw = (sessionId, method, params = {}) => b.ws.send(JSON.stringify({ id: ++id, sessionId, method, params }));
	const auto = { autoAttach: true, waitForDebuggerOnStart: false, flatten: true };
	b.ws.addEventListener('message', (ev) => {
		const { method, params } = JSON.parse(ev.data);
		if (method === 'Target.attachedToTarget') {
			raw(params.sessionId, 'Network.enable');
			if (block.length) raw(params.sessionId, 'Network.setBlockedURLs', { urls: block });
			raw(params.sessionId, 'Target.setAutoAttach', auto);
		} else if (method === 'Network.requestWillBeSent') {
			seen.push(params.request.url);
		}
	});
	await b.send('Network.enable');
	if (block.length) await b.send('Network.setBlockedURLs', { urls: block });
	await b.send('Target.setAutoAttach', auto);
	await b.send('Page.addScriptToEvaluateOnNewDocument', {
		source: `addEventListener('nautilus-playground-ready', () => (window.__playgroundReady = true));`
	});
	return seen;
}

const statusBar = `[...document.querySelectorAll('.statusbar-item')].map((e) => e.innerText.replace(/\\s+/g, ' ').trim()).filter(Boolean).join(' | ')`;
const engineAnswers = `Promise.race([window.nautilusPlayground.files().then((f) => Object.keys(f).length), new Promise((r) => setTimeout(() => r(null), 5000))])`;

async function open(b, example, deadlineMs = 150000) {
	const t0 = Date.now();
	const until = async (what, check) => {
		while (Date.now() - t0 < deadlineMs) {
			if (await check()) return;
			await new Promise((r) => setTimeout(r, 250));
		}
		assert.fail(`${example}: no "${what}" within ${deadlineMs / 1000} s\nconsole:\n  ${b.consoleLines.slice(-40).join('\n  ')}`);
	};
	await b.navigate(`${base}/app/?example=${example}`);
	await until('workbench ready', () => b.eval('window.__playgroundReady === true'));
	await until('engine worker answers', () => b.eval(engineAnswers));
	await until('nautilus: live', async () => /nautilus: live/i.test(await b.eval(statusBar)));
}


/** Page-side helper: open a view in an iframe (sw.js controls pages under
 * view/, not fetches from the workbench) and report its status-less result:
 * the document it got. A 500 from the engine is an HTML page too. */
const openView = `(url) => new Promise((resolve, reject) => {
	const f = document.createElement('iframe');
	f.style.cssText = 'position:fixed;width:800px;height:600px;left:0;top:0;z-index:99999';
	f.onload = () => resolve({ title: f.contentDocument.title, html: f.contentDocument.documentElement.outerHTML.slice(0, 600), text: f.contentDocument.body ? f.contentDocument.body.innerText.slice(0, 600) : '' });
	f.onerror = () => reject(new Error('iframe failed'));
	f.src = url;
	document.body.appendChild(f);
})`;

const builder = (urls) => urls.filter((u) => /naut-hmi\.wasm|hmi-packs\.tgz|hmi-worker\.js/.test(u));

test('a prebuilt screen never fetches the screen builder', { timeout: 300000 }, async () => {
	const b = await Browser.launch();
	try {
		const seen = await watch(b);
		await open(b, 'lift-station');
		await new Promise((r) => setTimeout(r, 5000)); // the old eager build started within a second of load
		assert.deepEqual(builder(seen), [], 'the builder or its packages were fetched with no screen asked for');
		assert.ok(seen.some((u) => /\/naut\.wasm/.test(u)), 'naut.wasm itself was never fetched: the watcher saw nothing');
	} finally {
		await b.close();
	}
});

test('a screen the browser builds: the builder is fetched on first ask, and the engine stays responsive', { timeout: 400000 }, async (t) => {
	const b = await Browser.launch();
	try {
		const seen = await watch(b);
		await open(b, 'demo');
		assert.deepEqual(builder(seen), [], 'the builder was fetched before the screen was asked for');
		const r = await b.eval(`(async () => {
			const url = window.nautilusPlayground.views?.hmi;
			if (!url) return { error: 'no screen view' };
			// ping the engine's worker (the one running the scan loop) while the screen builds
			let worst = 0, pings = 0, stop = false;
			const pinger = (async () => {
				while (!stop) {
					const t = performance.now();
					await window.nautilusPlayground.files();
					worst = Math.max(worst, performance.now() - t);
					pings++;
					await new Promise((r) => setTimeout(r, 50));
				}
			})();
			const t0 = performance.now();
			const doc = await (${openView})(url);
			const ms = performance.now() - t0;
			stop = true;
			await pinger;
			return { ...doc, ms, worst, pings };
		})()`);
		assert.equal(r.error, undefined, r.error);
		t.diagnostic(`first screen: ${(r.ms / 1000).toFixed(1)} s from ask to HTML; engine worker's slowest answer during the build ${r.worst.toFixed(0)} ms over ${r.pings} pings`);
		assert.doesNotMatch(r.text, /didn.t build|Still building|No operator screen|Not found/, r.text);
		assert.match(r.html, /<html/i);
		assert.ok(r.html.length > 100, r.html);
		assert.ok(builder(seen).some((u) => /naut-hmi\.wasm/.test(u)), 'the screen was built without fetching naut-hmi.wasm');
		assert.ok(builder(seen).some((u) => /hmi-packs\.tgz/.test(u)), 'the screen was built without its packages');
	} finally {
		await b.close();
	}
});

test('with the builder blocked, the screen says why and the controller keeps running', { timeout: 300000 }, async () => {
	const b = await Browser.launch();
	try {
		await watch(b, ['*naut-hmi.wasm*']);
		await open(b, 'demo');
		const r = await b.eval(`(async () => {
			const doc = await (${openView})(window.nautilusPlayground.views.hmi);
			return { ...doc, files: await window.nautilusPlayground.files().then((f) => Object.keys(f).length) };
		})()`);
		assert.match(r.text, /screen builder couldn.t be downloaded or started/, r.text);
		assert.ok(r.files > 0, 'the engine stopped answering after the builder failed');
		assert.match(await b.eval(statusBar), /nautilus: live/i);
	} finally {
		await b.close();
	}
});
