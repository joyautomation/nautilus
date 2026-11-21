// What leaves the browser when an error is reported (#1073). The docs site
// and the playground send JavaScript errors to our own GlitchTip and nothing
// else: a stack, the page's PATH, the browser, the build. This module is the
// last gate (the SDK's beforeSend): it builds the event again from an
// ALLOWLIST of fields instead of deleting the ones we know are sensitive, so
// a field a later SDK adds is dropped until someone allows it here.
//
// Never sent, whatever the SDK collected: query strings and #fragments (a
// share link's #project=<code> IS the visitor's whole project), the
// Referer, cookies, request headers other than User-Agent, the user
// object (so no IP: the SDK does not ask for one, and the GlitchTip project
// scrubs the connection's address too), breadcrumbs, `extra`, modules,
// server_name, and any exception text beyond its first line.
//
// What it cannot promise: an error message is text a program wrote. We keep
// one line of at most 200 characters with long quoted strings replaced, but a
// short message could still name something from a visitor's project. If one
// ever does, the fix belongs here, with a case in scrub.test.mjs.

const MAX_TEXT = 200;
const MAX_FRAMES = 50;
const MAX_QUOTED = 24;

// A saved project's id is in the path (/playground/p/<12 chars>): the path
// is kept, the id is not.
const SHARE_PATH = /(\/playground\/p\/)[a-z2-7]{12}(\/?)$/;

/** A URL as scheme + host + path, no query, no fragment, no share id. */
export function cleanUrl(u, base = 'http://invalid.invalid/') {
	if (typeof u !== 'string' || u === '') return u;
	let url;
	try {
		url = new URL(u, base);
	} catch {
		return '';
	}
	// blob:, data:, javascript: and extensions carry content or identifiers.
	if (url.protocol !== 'http:' && url.protocol !== 'https:') return `${url.protocol}…`;
	const path = url.pathname.replace(SHARE_PATH, '$1:id$2');
	return `${url.origin}${path}`;
}

/** One line of an error message: URLs reduced to their path, long quoted
 * strings replaced, cut at MAX_TEXT. A rejected non-Error is reported as
 * what it is, never as its serialized value. */
export function cleanText(s) {
	if (typeof s !== 'string') return s;
	// The SDK's texts for a thrown non-Error: they carry the value, or the
	// object's property names ("Object captured as promise rejection with keys: ...").
	if (/\bcaptured (as (promise rejection|exception)|with (value|keys))\b/i.test(s)) return 'Non-Error value thrown';
	let t = s.split(/\r?\n/, 1)[0];
	t = t.replace(/https?:\/\/[^\s"'`)<>]+/g, (m) => cleanUrl(m));
	t = t.replace(/(["'`])((?:\\.|(?!\1).)*)\1/g, (m, q, body) => (body.length > MAX_QUOTED ? `${q}…${q}` : m));
	return t.length > MAX_TEXT ? `${t.slice(0, MAX_TEXT)}…` : t;
}

const pick = (o, keys) => {
	const out = {};
	for (const k of keys) if (o && o[k] !== undefined) out[k] = o[k];
	return out;
};

function frame(f, base) {
	const out = pick(f, ['function', 'lineno', 'colno', 'in_app']);
	if (f.filename) out.filename = cleanUrl(f.filename, base);
	return out;
}

function exceptionValue(v, base) {
	const out = pick(v, ['type']);
	if (v.value !== undefined) out.value = cleanText(v.value);
	if (v.mechanism) out.mechanism = pick(v.mechanism, ['type', 'handled', 'source', 'is_exception_group', 'exception_id', 'parent_id']);
	if (v.stacktrace?.frames) out.stacktrace = { frames: v.stacktrace.frames.slice(-MAX_FRAMES).map((f) => frame(f, base)) };
	return out;
}

function ctx(c, keys) {
	return c ? pick(c, keys) : undefined;
}

/** The SDK's beforeSend: the event to send, or null to send nothing. */
export function scrubEvent(event, { base = 'http://invalid.invalid/', userAgent } = {}) {
	if (!event || typeof event !== 'object') return null;
	const out = pick(event, ['event_id', 'timestamp', 'platform', 'level', 'release', 'environment']);
	if (event.sdk) out.sdk = pick(event.sdk, ['name', 'version']);
	if (event.exception?.values) out.exception = { values: event.exception.values.map((v) => exceptionValue(v, base)) };
	if (typeof event.message === 'string') out.message = cleanText(event.message);
	const url = event.request?.url ?? base;
	out.request = { url: cleanUrl(url, base) };
	const ua = userAgent ?? event.request?.headers?.['User-Agent'];
	if (ua) out.request.headers = { 'User-Agent': ua };
	const contexts = {};
	const browser = ctx(event.contexts?.browser, ['name', 'version']);
	const os = ctx(event.contexts?.os, ['name', 'version']);
	if (browser) contexts.browser = browser;
	if (os) contexts.os = os;
	if (Object.keys(contexts).length) out.contexts = contexts;
	const app = event.tags?.app;
	if (app === 'docs' || app === 'playground') out.tags = { app };
	return out;
}
