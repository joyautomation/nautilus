// The error reporter (website/src/errors, #1073) in a real headless Chrome:
// built with a DSN that points at a stub server, loaded into a page that
// does everything the SDK would normally record (console output, clicks,
// fetches, a share-link URL), then made to fail. What the stub receives is
// what a visitor's browser would send to GlitchTip:
//
//     node --test playground/app/errors.browser.test.mjs
//
// Needs `npm ci` in website/ (the playground job in ci.yml has it).
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser } from '../../tools/vscode-iec/webview-ui/gesture-harness/cdp.mjs';

const website = fileURLToPath(new URL('../../website/', import.meta.url));
const haveSdk = existsSync(join(website, 'node_modules/@sentry/browser'));
const SECRET = 'SECRET-PROJECT-SOURCE';

let server, port, tmp, withDsn, withoutDsn;
const requests = []; // every request the stub sees: { method, path, body }

function build(dir, args) {
	return execFileSync('node', [join(website, 'scripts/build-errors.mjs'), '--out', dir, ...args], { cwd: website, encoding: 'utf8' });
}

// The page: noise the SDK records by default (console, DOM, fetch), then two
// failures. The URL it is served at carries a share id, a query and a #fragment.
const page = (script) => `<!doctype html><meta charset=utf-8><title>t</title>
<button id=b>press ${SECRET}</button>
<script src="${script}"></script>
<script>
console.log('${SECRET} on the console');
document.getElementById('b').click();
fetch('/ping?token=${SECRET}').catch(() => {});
setTimeout(() => { throw new TypeError('boom "${'q'.repeat(40)}" at ' + location.href + '\\nsecond line ${SECRET}'); }, 300);
setTimeout(() => { Promise.reject({ code: '${SECRET}' }); }, 450);
// An error the reporter filters out: with client reports on, the SDK would
// tell the server it dropped one, when the page is hidden.
setTimeout(() => { throw new Error('ResizeObserver loop completed with undelivered notifications.'); }, 600);
setTimeout(() => { window.__done = true; }, 1200);
</script>`;

before(async () => {
	if (!haveSdk) return;
	tmp = mkdtempSync(join(tmpdir(), 'naut-errors-'));
	await new Promise((r) => {
		server = createServer((req, res) => {
			const chunks = [];
			req.on('data', (c) => chunks.push(c));
			req.on('end', () => {
				const path = req.url;
				requests.push({ method: req.method, path, body: Buffer.concat(chunks).toString('utf8') });
				res.setHeader('access-control-allow-origin', '*');
				res.setHeader('access-control-allow-headers', '*');
				if (req.method === 'OPTIONS') return res.writeHead(204).end();
				if (path.startsWith('/api/1/envelope')) return res.writeHead(200, { 'content-type': 'application/json' }).end('{}');
				if (path.startsWith('/with/')) return res.writeHead(200, { 'content-type': 'text/html' }).end(page('/with.js'));
				if (path.startsWith('/without/')) return res.writeHead(200, { 'content-type': 'text/html' }).end(page('/without.js'));
				if (path === '/with.js') return res.writeHead(200, { 'content-type': 'text/javascript' }).end(readFileSync(withDsn));
				if (path === '/without.js') return res.writeHead(200, { 'content-type': 'text/javascript' }).end(readFileSync(withoutDsn));
				res.writeHead(200).end('ok');
			});
		});
		server.listen(0, '127.0.0.1', r);
	});
	port = server.address().port;
	build(join(tmp, 'with'), ['--dsn', `http://abcdef0123456789@127.0.0.1:${port}/1`, '--release', 'nautilus-docs@test']);
	build(join(tmp, 'without'), ['--dsn', '']);
	withDsn = join(tmp, 'with/errors.js');
	withoutDsn = join(tmp, 'without/errors.js');
});
after(() => {
	server?.close();
	if (tmp) rmSync(tmp, { recursive: true, force: true });
});

async function visit(prefix) {
	const b = await Browser.launch();
	try {
		await b.navigate(`http://127.0.0.1:${port}/${prefix}/playground/p/abcdefghijkl?project=${SECRET}&p=zzz#project=${SECRET}`);
		const end = Date.now() + 20000;
		while (Date.now() < end && (await b.eval('window.__done === true')) !== true) await new Promise((r) => setTimeout(r, 100));
		// A page going to the background is when the SDK flushes its client reports.
		await b.eval("Object.defineProperty(document, 'visibilityState', { get: () => 'hidden' }); document.dispatchEvent(new Event('visibilitychange')); true");
		await new Promise((r) => setTimeout(r, 1500)); // the SDK's transport is asynchronous
	} finally {
		await b.close();
	}
}

// An envelope is newline-separated JSON: a header, then (item header, payload) pairs.
function items(body) {
	const lines = body.split('\n').filter(Boolean).map((l) => JSON.parse(l));
	const out = [];
	for (let i = 1; i + 1 < lines.length; i += 2) out.push({ header: lines[i], payload: lines[i + 1] });
	return out;
}

test('with a DSN: only error events are sent, scrubbed to path, stack and browser', { skip: !haveSdk && 'npm ci in website/ first', timeout: 90000 }, async () => {
	requests.length = 0;
	await visit('with');
	const sent = requests.filter((r) => r.path.startsWith('/api/'));
	assert.ok(sent.length > 0, 'the reporter sent something: it is wired');
	for (const r of sent) assert.equal(r.method === 'POST' && r.path.startsWith('/api/1/envelope'), true, `unexpected request ${r.method} ${r.path}`);
	const all = sent.flatMap((r) => items(r.body));
	assert.deepEqual([...new Set(all.map((i) => i.header.type))], ['event'], 'event items only: no session, transaction, replay, client_report, log');
	for (const r of sent) assert.ok(!r.body.includes(SECRET), `a secret left the browser: ${r.body.slice(0, 600)}`);

	const events = all.map((i) => i.payload);
	const typeError = events.find((e) => e.exception?.values?.[0]?.type === 'TypeError');
	assert.ok(typeError, `the thrown TypeError was reported: ${JSON.stringify(events).slice(0, 600)}`);
	assert.equal(typeError.request.url, `http://127.0.0.1:${port}/with/playground/p/:id`, 'the path only: no share id, query or fragment');
	assert.equal(typeError.exception.values[0].value.split('\n').length, 1, 'one line of message');
	assert.ok(typeError.exception.values[0].value.includes('boom "…"'), 'a long quoted string is replaced');
	assert.ok(typeError.exception.values[0].stacktrace.frames.length > 0, 'it has a stack');
	assert.equal(typeError.release, 'nautilus-docs@test');
	assert.deepEqual(typeError.tags, { app: 'docs' });
	const rejection = events.find((e) => e.exception?.values?.[0]?.value === 'Non-Error value thrown');
	assert.ok(rejection, 'the non-Error rejection is reported without its value: ' + JSON.stringify(events.map((e) => e.exception?.values?.[0]?.value)));
	for (const e of events) {
		for (const gone of ['user', 'breadcrumbs', 'extra', 'modules', 'server_name', 'transaction']) assert.equal(e[gone], undefined, `${gone} in an event`);
		assert.deepEqual(Object.keys(e.request), ['url', 'headers']);
		assert.deepEqual(Object.keys(e.request.headers), ['User-Agent']);
		assert.ok(!JSON.stringify(e).includes('203.0.113'), 'no address');
	}
	// The envelope's own header must not carry a user or a trace either.
	for (const r of sent) {
		const head = JSON.parse(r.body.split('\n', 1)[0]);
		assert.deepEqual(Object.keys(head).sort().filter((k) => !['event_id', 'sent_at', 'sdk', 'dsn'].includes(k)), [], `envelope header: ${JSON.stringify(head)}`);
	}
});

test('without a DSN: the page makes no request to the error server', { skip: !haveSdk && 'npm ci in website/ first', timeout: 90000 }, async () => {
	requests.length = 0;
	await visit('without');
	assert.deepEqual(requests.filter((r) => r.path.startsWith('/api/')), [], 'nothing was sent');
	assert.match(readFileSync(withoutDsn, 'utf8'), /no error-reporting DSN/);
});
