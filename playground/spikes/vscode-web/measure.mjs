// Cold-load a page in Chromium with the CPU throttled, and report when it
// was usable, when the nautilus program was scanning, and how many bytes it
// fetched (raw, and brotli-compressed as a production server would send).
//
//   node measure.mjs <name> <url> <readyText> <throttle> [paintSelector]
import { createRequire } from 'node:module';
import { brotliCompressSync, constants } from 'node:zlib';

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PW || 'playwright');

const [name, url, readyText, throttleArg, paintSel] = process.argv.slice(2);
const throttle = Number(throttleArg || 1);

const browser = await chromium.launch({ headless: true, args: process.env.NOISOLATE ? ['--disable-site-isolation-trials', '--disable-features=IsolateOrigins,site-per-process'] : [] });
const context = await browser.newContext({ viewport: { width: 1366, height: 768 } });
const page = await context.newPage();
const cdp = await context.newCDPSession(page);
await cdp.send('Network.enable');
await cdp.send('Network.setCacheDisabled', { cacheDisabled: true });
await cdp.send('Emulation.setCPUThrottlingRate', { rate: throttle });

const bodies = [];
const urls = new Map();
// Workers (VS Code's extension host, our engine) fetch too: attach to them.
await cdp.send('Target.setAutoAttach', { autoAttach: true, waitForDebuggerOnStart: false, flatten: true });
page.on('response', async (res) => {
	try {
		if (res.request().method() !== 'GET') return;
		const buf = await res.body();
		bodies.push({ url: res.url(), raw: buf.length, br: brotli(buf) });
	} catch {
		/* redirects, aborted, worker-owned: counted below via CDP */
	}
});
context.on('serviceworker', () => {});
cdp.on('Network.responseReceived', (e) => urls.set(e.requestId, e.response.url));
const cdpBytes = new Map();
cdp.on('Network.loadingFinished', (e) => cdpBytes.set(e.requestId, e.encodedDataLength));

function brotli(buf) {
	if (buf.length < 64) return buf.length;
	return brotliCompressSync(buf, { params: { [constants.BROTLI_PARAM_QUALITY]: 9 } }).length;
}

const t0 = Date.now();
await page.goto(url, { waitUntil: 'commit' });
let tPaint = null;
if (paintSel) {
	await page.waitForSelector(paintSel, { timeout: 180000 });
	tPaint = Date.now() - t0;
}
await page.waitForFunction((t) => document.body && document.body.innerText.includes(t), readyText, { timeout: 240000, polling: 100 });
const tReady = Date.now() - t0;
// Let late fetches land, then fetch anything the page-level listener missed
// (worker-owned requests) to size it ourselves.
await page.waitForTimeout(1500);
const seen = new Set(bodies.map((b) => b.url));
for (const u of new Set(urls.values())) {
	if (seen.has(u) || !/^https?:/.test(u)) continue;
	try {
		const r = await fetch(u);
		const buf = Buffer.from(await r.arrayBuffer());
		bodies.push({ url: u, raw: buf.length, br: brotli(buf), viaWorker: true });
		seen.add(u);
	} catch {
		/* not refetchable */
	}
}
const raw = bodies.reduce((a, b) => a + b.raw, 0);
const br = bodies.reduce((a, b) => a + b.br, 0);
const wasm = bodies.filter((b) => b.url.includes('.wasm')).reduce((a, b) => a + b.br, 0);
const top = [...bodies].sort((a, b) => b.br - a.br).slice(0, 6).map((b) => `${(b.br / 1e6).toFixed(2)}MB ${b.url.replace(/^https?:\/\/[^/]+/, '').slice(0, 90)}`);
console.log(JSON.stringify({ name, throttle, paintMs: tPaint, readyMs: tReady, requests: bodies.length, rawMB: +(raw / 1e6).toFixed(2), brMB: +(br / 1e6).toFixed(2), wasmBrMB: +(wasm / 1e6).toFixed(2), top }));
await browser.close();
