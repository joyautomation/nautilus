// The playground connects to its engine, in a real (headless) Chrome over
// the built playground (run after scripts/build.sh):
//
//     node --test playground/app/live.browser.test.mjs
//
// The nautilus extension talks to naut.wasm, in a worker, as it would to a
// controller at http://playground.nautilus: its requests cross the page's
// bridge to the engine, which answers them through the tag API, Host guard
// included. Any break on that path (#891: every request reached the guard
// addressed to example.com and got 403) leaves the status bar on
// "nautilus: offline". So: open examples and wait for "nautilus: live".
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

/** Attach to every worker and frame (the extension host and the engine run
 * in workers): their console goes to b.consoleLines, and every request that
 * fails or answers >= 400, anywhere, to the returned list. */
async function watch(b) {
	const failed = [];
	const urls = new Map();
	let id = 1e6;
	const raw = (sessionId, method, params = {}) => b.ws.send(JSON.stringify({ id: ++id, sessionId, method, params }));
	const auto = { autoAttach: true, waitForDebuggerOnStart: false, flatten: true };
	b.ws.addEventListener('message', (ev) => {
		const { method, params, sessionId = '' } = JSON.parse(ev.data);
		if (method === 'Target.attachedToTarget') {
			b.consoleLines.push(`[attached ${params.targetInfo.type}] ${params.targetInfo.url.slice(0, 120)}`);
			for (const m of ['Runtime.enable', 'Network.enable']) raw(params.sessionId, m);
			raw(params.sessionId, 'Target.setAutoAttach', auto);
		} else if (method === 'Network.requestWillBeSent') {
			urls.set(sessionId + params.requestId, params.request.url);
		} else if (method === 'Network.responseReceived' && params.response.status >= 400) {
			failed.push(`${params.response.status} ${params.response.url}`);
		} else if (method === 'Network.loadingFailed' && !params.canceled) {
			failed.push(`${params.errorText} ${urls.get(sessionId + params.requestId) ?? '(unknown url)'}`);
		}
	});
	await b.send('Network.enable');
	await b.send('Target.setAutoAttach', auto);
	// main.js fires this once the workbench is up and the project loaded.
	await b.send('Page.addScriptToEvaluateOnNewDocument', {
		source: `addEventListener('nautilus-playground-ready', () => (window.__playgroundReady = true));`
	});
	return failed;
}

const statusBar = `[...document.querySelectorAll('.statusbar-item')].map((e) => e.innerText.replace(/\\s+/g, ' ').trim()).filter(Boolean).join(' | ')`;
// The engine worker, asked directly for the project's files: a number once
// it answers, null if it does not within 5 s.
const engineAnswers = `Promise.race([window.nautilusPlayground.files().then((f) => Object.keys(f).length), new Promise((r) => setTimeout(() => r(null), 5000))])`;

for (const example of ['lift-station', 'epic-bench']) {
	test(`${example}: the status bar reads "nautilus: live"`, { timeout: 300000 }, async () => {
		const b = await Browser.launch();
		try {
			const failed = await watch(b);
			const t0 = Date.now();
			const deadline = t0 + 150000;
			const stages = [];
			const until = async (stage, check) => {
				while (Date.now() < deadline) {
					const v = await check();
					if (v) return stages.push(`${stage} at ${((Date.now() - t0) / 1000).toFixed(1)} s`), v;
					await new Promise((r) => setTimeout(r, 250));
				}
				const page = await b.eval(`JSON.stringify({ title: document.title, workbench: !!document.querySelector('.monaco-workbench'), statusBar: ${statusBar} })`).catch((e) => String(e));
				assert.fail(
					`${example}: no "${stage}" within ${(deadline - t0) / 1000} s` +
						`\nreached: ${stages.join(', ') || 'nothing'}\npage: ${page}` +
						`\nfailed requests:\n  ${failed.join('\n  ') || '(none)'}` +
						`\nconsole (pages, frames, workers):\n  ${b.consoleLines.slice(-60).join('\n  ')}`
				);
			};
			await b.navigate(`${base}/app/?example=${example}`);
			await until('workbench ready', () => b.eval('window.__playgroundReady === true'));
			await until('engine worker answers', () => b.eval(engineAnswers));
			await until('nautilus: live', async () => /nautilus: live/i.test(await b.eval(statusBar)));
		} finally {
			await b.close();
		}
	});
}
