// The scrubber (scrub.mjs) is the last gate before an error report leaves the
// browser (#1073). Run: node --test website/src/errors/scrub.test.mjs (CI's
// changes job runs it on every PR, light or not).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { cleanText, cleanUrl, scrubEvent } from './scrub.mjs';

const BASE = 'https://nautilus.joyautomation.com/playground/engine/app/';
const SECRET = 'SECRET-PROJECT-SOURCE';

// An event as the SDK builds it, loaded with everything that must not leave.
const raw = () => ({
	event_id: 'abc123',
	timestamp: 1763600000.5,
	platform: 'javascript',
	level: 'error',
	release: 'nautilus-docs@9de55db3',
	environment: 'production',
	sdk: { name: 'sentry.javascript.browser', version: '10.78.0', packages: [{ name: 'npm:@sentry/browser' }] },
	exception: {
		values: [
			{
				type: 'TypeError',
				value: `x is not a function\nsecond line ${SECRET}`,
				mechanism: { type: 'onerror', handled: false },
				stacktrace: {
					frames: [
						{
							filename: `https://nautilus.joyautomation.com/playground/engine/app/assets/main-1a2b.js?v=0.22-abc#${SECRET}`,
							abs_path: `https://nautilus.joyautomation.com/x.js#${SECRET}`,
							function: 'run',
							lineno: 10,
							colno: 5,
							in_app: true,
							pre_context: [SECRET],
							context_line: SECRET,
							vars: { secret: SECRET }
						}
					]
				}
			}
		]
	},
	request: {
		url: `https://nautilus.joyautomation.com/playground/p/abcdefghijkl?project=https://x/${SECRET}&p=zzz#project=${SECRET}`,
		headers: { Referer: `https://elsewhere.example/?q=${SECRET}`, 'User-Agent': 'UA/1.0', Cookie: `sid=${SECRET}` },
		cookies: { sid: SECRET },
		query_string: `p=${SECRET}`
	},
	user: { id: SECRET, email: `${SECRET}@example.com`, ip_address: '203.0.113.9' },
	breadcrumbs: [{ category: 'console', message: SECRET }, { category: 'ui.click', message: `button ${SECRET}` }],
	extra: { __serialized__: { code: SECRET } },
	modules: { leak: SECRET },
	server_name: `host-${SECRET}`,
	transaction: `/playground/p/${SECRET}`,
	contexts: {
		browser: { name: 'Chrome', version: '142.0.0', extra: SECRET },
		os: { name: 'Linux', version: '6', kernel_version: SECRET },
		device: { model: SECRET },
		culture: { locale: 'en-US', timezone: 'America/Denver' },
		trace: { trace_id: SECRET }
	},
	tags: { app: 'playground', other: SECRET },
	fingerprint: [SECRET]
});

test('nothing but the allowlist survives, and no secret appears anywhere in the event', () => {
	const out = scrubEvent(raw(), { base: BASE });
	assert.ok(!JSON.stringify(out).includes(SECRET), JSON.stringify(out));
	assert.deepEqual(Object.keys(out).sort(), ['contexts', 'environment', 'event_id', 'exception', 'level', 'platform', 'release', 'request', 'sdk', 'tags', 'timestamp']);
	for (const gone of ['user', 'breadcrumbs', 'extra', 'modules', 'server_name', 'transaction', 'fingerprint']) assert.equal(out[gone], undefined, gone);
	assert.deepEqual(out.contexts, { browser: { name: 'Chrome', version: '142.0.0' }, os: { name: 'Linux', version: '6' } });
	assert.deepEqual(out.tags, { app: 'playground' });
	assert.deepEqual(out.sdk, { name: 'sentry.javascript.browser', version: '10.78.0' });
});

test('the page URL is the path only: no query, no #fragment, no share id', () => {
	const out = scrubEvent(raw(), { base: BASE });
	assert.equal(out.request.url, 'https://nautilus.joyautomation.com/playground/p/:id');
	assert.deepEqual(out.request.headers, { 'User-Agent': 'UA/1.0' }); // no Referer, no Cookie
	assert.equal(out.request.cookies, undefined);
	assert.equal(out.request.query_string, undefined);
});

test('a #project= share link, the whole project, never survives', () => {
	const e = raw();
	e.request.url = `https://nautilus.joyautomation.com/playground/#project=${SECRET}`;
	assert.equal(scrubEvent(e, { base: BASE }).request.url, 'https://nautilus.joyautomation.com/playground/');
	assert.equal(cleanUrl(`https://nautilus.joyautomation.com/playground/engine/app/?project=/__lesson/b1.json&tutorial=1`), 'https://nautilus.joyautomation.com/playground/engine/app/');
});

test('a share id is dropped wherever the path sits', () => {
	assert.equal(cleanUrl('https://h.example/playground/p/abcdefghijkl/'), 'https://h.example/playground/p/:id/');
	assert.equal(cleanUrl('https://h.example/base/playground/p/abcdefghijkl'), 'https://h.example/base/playground/p/:id');
	assert.equal(cleanUrl('https://h.example/playground/p/short'), 'https://h.example/playground/p/short');
});

test('frames keep function, line, column and the script path; the SDK-added source and vars go', () => {
	const f = scrubEvent(raw(), { base: BASE }).exception.values[0].stacktrace.frames[0];
	assert.deepEqual(f, {
		function: 'run',
		lineno: 10,
		colno: 5,
		in_app: true,
		filename: 'https://nautilus.joyautomation.com/playground/engine/app/assets/main-1a2b.js'
	});
});

test('an exception message is one short line; URLs reduce to paths; long quoted strings go', () => {
	assert.equal(scrubEvent(raw(), { base: BASE }).exception.values[0].value, 'x is not a function');
	assert.equal(cleanText('failed to fetch https://h.example/a/b?token=SECRET#frag now'), 'failed to fetch https://h.example/a/b now');
	assert.equal(cleanText(`bad tag "Pump1_Speed" and "${'x'.repeat(30)}"`), 'bad tag "Pump1_Speed" and "…"');
	assert.equal(cleanText(`unexpected token \`${'y'.repeat(40)}\``), 'unexpected token `…`');
	const long = cleanText('e'.repeat(500));
	assert.equal(long.length, 201);
	assert.ok(long.endsWith('…'));
});

test('a rejected non-Error is reported as that and never as its value', () => {
	assert.equal(cleanText(`Non-Error promise rejection captured with value: {"code":"${SECRET}"}`), 'Non-Error value thrown');
	assert.equal(cleanText(`Non-Error exception captured with keys: ${SECRET}`), 'Non-Error value thrown');
	assert.equal(cleanText(`Object captured as promise rejection with keys: ${SECRET}, other`), 'Non-Error value thrown');
	assert.equal(cleanText('Event `CustomEvent` (type=boom) captured as exception'), 'Non-Error value thrown');
});

test('an unusual event still comes out clean: no exception, a message, a blob: or data: URL', () => {
	const out = scrubEvent({ message: `oops ${'z'.repeat(300)}\nmore`, request: { url: `blob:https://nautilus.joyautomation.com/${SECRET}` } }, { base: BASE });
	assert.ok(!JSON.stringify(out).includes(SECRET));
	assert.equal(out.request.url, 'blob:…');
	assert.ok(out.message.length <= 201);
	assert.equal(cleanUrl(`data:text/plain,${SECRET}`), 'data:…');
	assert.equal(scrubEvent(null), null);
});

test('a tag other than docs or playground is dropped', () => {
	const e = raw();
	e.tags = { app: SECRET };
	assert.equal(scrubEvent(e, { base: BASE }).tags, undefined);
});
