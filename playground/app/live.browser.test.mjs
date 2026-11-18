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

/** Collect the console of every worker and frame too (the extension host
 * and the engine run in workers), into b.consoleLines. */
async function attachWorkers(b) {
	let id = 1e6;
	const raw = (sessionId, method, params = {}) => b.ws.send(JSON.stringify({ id: ++id, sessionId, method, params }));
	const auto = { autoAttach: true, waitForDebuggerOnStart: false, flatten: true };
	b.on('Target.attachedToTarget', ({ sessionId, targetInfo }) => {
		b.consoleLines.push(`[attached ${targetInfo.type}] ${targetInfo.url}`);
		raw(sessionId, 'Runtime.enable');
		raw(sessionId, 'Target.setAutoAttach', auto);
	});
	await b.send('Target.setAutoAttach', auto);
}

for (const example of ['lift-station', 'epic-bench']) {
	test(`${example}: the status bar reads "nautilus: live"`, { timeout: 180000 }, async () => {
		const b = await Browser.launch();
		try {
			await attachWorkers(b);
			await b.navigate(`${base}/app/?example=${example}`);
			let text = '';
			for (const until = Date.now() + 120000; Date.now() < until; ) {
				text = await b.eval(`[...document.querySelectorAll('.statusbar-item')].map((e) => e.innerText.replace(/\\s+/g, ' ').trim()).filter(Boolean).join(' | ')`);
				if (/nautilus: live/i.test(text)) return;
				await new Promise((r) => setTimeout(r, 500));
			}
			assert.fail(`${example}: the status bar never read "nautilus: live"; it showed: ${text}\n` + b.consoleLines.slice(-60).join('\n'));
		} finally {
			await b.close();
		}
	});
}
