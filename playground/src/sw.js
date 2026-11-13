// The playground's web server. On a real machine `naut run` serves the
// built-in dashboard, or the project's HMI (server.hmi in nautilus.yaml),
// and the tag API beside it. Here the controller is naut.wasm in the
// playground tab, so this service worker plays that server for pages under
// view/, in tabs of their own:
//
//   view/dashboard/?engine=<channel>      the built-in dashboard
//   view/<name>/?engine=<channel>         a prebuilt HMI (hmi/<name>/, built
//                                         with paths.base = <scope><name>)
//   view/app/?engine=<channel>            the project's own screen, which the
//                                         engine builds (naut's HMI build)
//
// "/api/..." goes to the engine over the playground tab's bridge channel
// (bridge.js; the channel names that tab), the dashboard comes from the
// engine too, and an HMI's own files are static, with the SPA fallback to
// index.html that server.hmi gives it. A page's later requests (its
// "/api/..." calls are absolute) are matched to it by client id.

const SCOPE = new URL(self.registration.scope);
const ENGINE = new URL('../', SCOPE); // where hmi/<name>/ is published
const CALL_TIMEOUT_MS = 8000;
const APP = 'app'; // the view of the project's own screen, built by the engine
const QUIET_MS = 15000; // a stream this quiet asks whether the engine is still there
const MEMO = 'nautilus-view-clients';

/** clientId -> {name, channel}; also kept in Cache Storage, since the
 * browser stops an idle service worker and its memory with it. */
const views = new Map();

self.addEventListener('install', () => self.skipWaiting());
// The newest playground tab says which channel it serves: a view whose own
// tab was reloaded (a new channel) or closed moves to it.
self.addEventListener('message', (e) => {
	if (e.data?.kind === 'engine' && e.data.channel) remember('latest', { channel: e.data.channel });
});
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', (event) => {
	const req = event.request;
	const url = new URL(req.url);
	if (url.origin !== SCOPE.origin) return;
	if (req.mode === 'navigate') {
		if (!url.href.startsWith(SCOPE.href)) return;
		event.respondWith(navigate(event, url));
		return;
	}
	event.respondWith(
		viewOf(event.clientId).then((view) => (view ? serve(view, req, url) : fetch(req)))
	);
});

async function navigate(event, url) {
	const name = url.pathname.slice(SCOPE.pathname.length).split('/')[0];
	let channel = url.searchParams.get('engine');
	if (!channel) channel = (await recall('last:' + name))?.channel; // a reload or a deep link
	if (!name || !channel) return page(404, 'This view needs the playground tab that runs its controller.');
	// A page reloaded after its playground tab was: go straight to the newest
	// one rather than wait out a dead channel. (Probed, not assumed: a view of
	// an older playground tab that is still open stays with it.)
	const latest = (await recall('latest'))?.channel;
	if (latest && latest !== channel && !(await call(channel, 'stats', [], 1500).then(() => true, () => false))) {
		channel = latest;
	}
	const view = { name, channel };
	remember('last:' + name, view);
	if (event.resultingClientId) remember(event.resultingClientId, view);
	void forgetClosed(event.resultingClientId);
	return serve(view, event.request, url);
}

/** Drop what is remembered for pages that are gone (keep: the page now
 * loading, which has no client to find yet). */
async function forgetClosed(keep) {
	try {
		const memo = await caches.open(MEMO);
		for (const req of await memo.keys()) {
			const key = decodeURIComponent(new URL(req.url).pathname.split('/_memo/')[1] ?? '');
			if (!key || key === keep || key === 'latest' || key.startsWith('last:')) continue;
			if (!(await self.clients.get(key))) {
				views.delete(key);
				await memo.delete(req);
			}
		}
	} catch {
		/* a memo is a convenience */
	}
}

async function viewOf(clientId) {
	if (clientId) {
		const known = views.get(clientId) ?? (await recall(clientId));
		if (known) return known;
		// A page from before this worker started, or a memo lost: its own URL says.
		const client = await self.clients.get(clientId);
		if (client && client.url.startsWith(SCOPE.href)) {
			const u = new URL(client.url);
			const name = u.pathname.slice(SCOPE.pathname.length).split('/')[0];
			const channel = u.searchParams.get('engine') ?? (await recall('last:' + name))?.channel;
			if (name && channel) {
				remember(clientId, { name, channel });
				return { name, channel };
			}
		}
	}
	return null;
}

async function serve(view, req, url) {
	try {
		return await answer(view, req, url);
	} catch (e) {
		const latest = (await recall('latest'))?.channel;
		if (latest && latest !== view.channel) {
			view.channel = latest;
			for (const [key, v] of views) if (v === view) remember(key, view);
			remember('last:' + view.name, view);
			try {
				return await answer(view, req, url);
			} catch {
				/* fall through to the message */
			}
		}
		return page(503, 'The playground tab that runs this controller is closed or not answering. Open the playground, then reload this page.', e);
	}
}

async function answer(view, req, url) {
	const prefix = SCOPE.pathname + view.name;
	let path = url.pathname;
	if (path === prefix || path.startsWith(prefix + '/')) path = path.slice(prefix.length) || '/';
	if (path.startsWith('/api/')) {
		if (path.startsWith('/api/stream') && req.method === 'GET') return stream(view.channel, path + url.search);
		return handle(view.channel, req.clone(), path + url.search);
	}
	if (view.name === 'dashboard') return handle(view.channel, req.clone(), path + url.search);
	if (view.name === APP) return app(view, req.clone(), path + url.search);
	return asset(view.name, req, path);
}

/** The project's own screen, as the engine built it (naut.wasm's app()):
 * its pages learn where they are served from (nx-base), so a link or a
 * reload finds them under view/app/. */
async function app(view, req, path) {
	const headers = {};
	req.headers.forEach((v, k) => (headers[k] = v));
	const body = req.method === 'GET' || req.method === 'HEAD' ? null : await req.text();
	const r = await call(view.channel, 'call', ['app', req.method, path, headers, body], 70000);
	const h = new Headers(r.headers);
	h.delete('content-length');
	let out = r.bytes ?? r.body;
	if (typeof out === 'string' && (h.get('content-type') || '').startsWith('text/html')) {
		out = out.replace('<meta name="nx-base" content="">', `<meta name="nx-base" content="${SCOPE.pathname}${APP}">`);
	}
	return new Response(req.method === 'HEAD' ? null : out, { status: r.status, headers: h });
}

/** One request to the engine's HTTP server. */
async function handle(channel, req, path) {
	const headers = {};
	req.headers.forEach((v, k) => (headers[k] = v));
	const body = req.method === 'GET' || req.method === 'HEAD' ? null : await req.text();
	const r = await call(channel, 'call', ['handle', req.method, path, headers, body]);
	const h = new Headers(r.headers);
	h.delete('content-length');
	h.delete('content-encoding');
	const empty = r.status === 204 || r.status === 304 || req.method === 'HEAD';
	return new Response(empty ? null : r.bytes ?? r.body, { status: r.status, headers: h });
}

/** An HMI's own file, or its index.html for a client-side route. */
async function asset(name, req, path) {
	let r = await fetch(new URL('hmi/' + name + path, ENGINE), { credentials: 'same-origin' });
	if (!r.ok && req.mode === 'navigate') r = await fetch(new URL('hmi/' + name + '/index.html', ENGINE));
	// A fresh Response, so its URL is the one asked for: a module's relative
	// imports resolve against its response URL, which must stay under view/.
	return new Response(r.body, { status: r.status, statusText: r.statusText, headers: r.headers });
}

/** The engine's server-sent events, as a streaming response. */
async function stream(channel, path) {
	await call(channel, 'stats', []); // a closed playground fails here, not as a silent stream
	const ch = new BroadcastChannel(channel);
	const sid = 'sw:' + crypto.randomUUID();
	const enc = new TextEncoder();
	let quiet;
	const end = () => {
		clearInterval(quiet);
		ch.close();
	};
	const body = new ReadableStream({
		start(ctrl) {
			let last = Date.now();
			ch.onmessage = ({ data: m }) => {
				if (m?.kind !== 'chunk' || m.sid !== sid) return;
				last = Date.now();
				if (m.chunk === null) {
					end();
					ctrl.close();
				} else ctrl.enqueue(enc.encode(m.chunk));
			};
			ch.postMessage({ kind: 'stream', sid, url: path });
			quiet = setInterval(() => {
				if (Date.now() - last < QUIET_MS) return;
				last = Date.now();
				call(channel, 'stats', []).catch((e) => {
					end();
					ctrl.error(e);
				});
			}, QUIET_MS);
		},
		cancel() {
			ch.postMessage({ kind: 'streamCancel', sid });
			end();
		}
	});
	return new Response(body, { headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' } });
}

/** A Playground method over the bridge. Ids are strings so they can never
 * match the extension's numeric ones on the same channel. */
function call(channel, op, args, timeout = CALL_TIMEOUT_MS) {
	return new Promise((resolve, reject) => {
		const ch = new BroadcastChannel(channel);
		const id = 'sw:' + crypto.randomUUID();
		const timer = setTimeout(() => {
			ch.close();
			reject(new Error('the playground engine did not answer'));
		}, timeout);
		ch.onmessage = ({ data: m }) => {
			if (m?.kind !== 'result' || m.id !== id) return;
			clearTimeout(timer);
			ch.close();
			if (m.ok) resolve(m.result);
			else reject(new Error(m.error));
		};
		ch.postMessage({ kind: 'call', id, op, args });
	});
}

function remember(key, view) {
	views.set(key, view);
	caches
		.open(MEMO)
		.then((c) => c.put(new Request(new URL('_memo/' + encodeURIComponent(key), SCOPE)), Response.json(view)))
		.catch(() => {});
}

async function recall(key) {
	if (views.has(key)) return views.get(key);
	try {
		const r = await (await caches.open(MEMO)).match(new URL('_memo/' + encodeURIComponent(key), SCOPE));
		const view = r ? await r.json() : null;
		if (view) views.set(key, view);
		return view;
	} catch {
		return null;
	}
}

function page(status, message, err) {
	const detail = err ? `<p class="d">${String(err.message || err).replace(/[<&]/g, (c) => (c === '<' ? '&lt;' : '&amp;'))}</p>` : '';
	return new Response(
		`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>nautilus</title>` +
			`<style>body{font:15px system-ui,sans-serif;background:#1f1f1f;color:#ccc;display:grid;place-items:center;min-height:100vh;margin:0;padding:16px;box-sizing:border-box}div{max-width:32em}.d{color:#888;font-size:13px}</style>` +
			`<div><p>${message}</p>${detail}</div>`,
		{ status, headers: { 'Content-Type': 'text/html; charset=utf-8' } }
	);
}
