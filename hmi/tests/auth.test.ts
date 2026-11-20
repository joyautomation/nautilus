// The kit's side of sign-in (#708 PR 7a). Each case is a way an operator
// could be misled or a page sent somewhere it must not go: a password
// prompt that loops or never comes, an action sent twice, a refusal shown
// as JSON, a sign-out nobody notices, a referral to a URL the controller
// did not name.
import { describe, it, expect } from './harness.js';
import {
	AuthSession,
	FOLLOW_GUARD_MS,
	FOLLOW_KEY,
	PROBE_MIN_MS,
	clockSkew,
	leaderTarget,
	markFollowed,
	mayFollow,
	sameOriginResponse,
	needsPassword,
	refusalMessage,
	signInHref,
	standbyLeader,
	wantsElevation,
	type Me,
	type PasswordAsk
} from '../src/lib/auth.js';
import { kitFetch, setKitFetch } from '../src/lib/kitfetch.js';
import { classify, SubstituteClient } from '../src/lib/substitutes.js';
import { PortParamClient } from '../src/lib/iolink.js';
import { fetchHistory } from '../src/lib/history.js';
import { readFileSync } from 'node:fs';

const json = (b: unknown, status = 200, headers: Record<string, string> = {}) =>
	new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json', ...headers } });

const PAGE = 'https://plc-a.example/screens/pump?x=1';
const SIGNIN = '/signin?next=%2Fscreens%2Fpump%3Fx%3D1';

const signedIn = (extra: Partial<Me> = {}): Me => ({
	auth: true,
	secure: true,
	principal: 'user:akhan',
	roles: ['engineer'],
	permissions: { '*': ['read', 'write', 'force', 'substitute'] },
	session: { expires: '2026-11-20T20:00:00Z', elevatedUntil: '2026-11-20T08:00:00Z' },
	elevated: ['force', 'edit', 'activate', 'deploy', 'config', 'users', 'override'],
	signIn: true,
	now: '2026-11-20T09:00:00Z',
	...extra
});
const anonymous = (extra: Partial<Me> = {}): Me => ({
	auth: true,
	secure: true,
	principal: 'anonymous',
	roles: [],
	permissions: {},
	signIn: true,
	...extra
});

type Sent = { url: string; method: string; body: string | undefined };
type Route = (url: string, init: RequestInit | undefined, n: number) => Response | Promise<Response>;

/**
 * A browser around an AuthSession: `routes` answer by path (each gets how
 * many times it was called before), `passwords` are typed in order (null
 * cancels; running out cancels).
 */
function world(routes: Record<string, Route>, passwords: (string | null)[] = [], opts: { href?: string; storage?: Map<string, string>; prompt?: () => Promise<string | null> } = {}) {
	const sent: Sent[] = [];
	const nav: string[] = [];
	const prompts: PasswordAsk[] = [];
	const store = opts.storage ?? new Map<string, string>();
	const calls = new Map<string, number>();
	let now = 1_000_000;
	const href = opts.href ?? PAGE;
	const fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
		const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
		const url = new URL(raw, href);
		const key = url.origin === new URL(href).origin ? url.pathname : url.href;
		sent.push({ url: key, method: init?.method ?? 'GET', body: typeof init?.body === 'string' ? init.body : undefined });
		const route = routes[key];
		if (!route) throw new Error('no route for ' + key);
		const n = calls.get(key) ?? 0;
		calls.set(key, n + 1);
		return route(key, init, n);
	};
	const loc = new URL(href);
	const s = new AuthSession({
		fetch,
		prompt: async (ask) => {
			prompts.push(ask);
			if (opts.prompt) return opts.prompt();
			return passwords.length ? (passwords.shift() as string | null) : null;
		},
		navigate: (h) => nav.push(h),
		location: () => ({ href, pathname: loc.pathname, search: loc.search }),
		storage: { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => void store.set(k, v) },
		now: () => now
	});
	return {
		s,
		sent,
		nav,
		prompts,
		store,
		to: (path: string) => sent.filter((x) => x.url === path),
		tick: (ms: number) => (now += ms)
	};
}

const ELEVATE_BODY = { error: 'confirm your password to do this: sign in again (/signin)', elevate: true };
/** A fresh elevation refusal each time (a Response body reads once). */
const elevate401 = () => json(ELEVATE_BODY, 401);
const WRITE = { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'T1', value: 5, requestId: 'r-1' }) };

describe('refusal text', () => {
	it("shows the controller's JSON error, not the JSON", () => {
		expect(refusalMessage(403, '{"error":"operator may not write TankSP"}')).toBe('operator may not write TankSP');
		expect(refusalMessage(401, '{"error":"confirm your password to do this","elevate":true}')).toBe('confirm your password to do this');
	});
	it('keeps plain text, and says something for an empty body', () => {
		expect(refusalMessage(400, 'unknown tag Nope\n')).toBe('unknown tag Nope');
		expect(refusalMessage(401, '')).toBe('sign in, or send a token');
		expect(refusalMessage(403, '')).toBe('not permitted');
		expect(refusalMessage(502, '', 'write failed')).toBe('write failed (502)');
		expect(refusalMessage(500, '{"other":1}')).toBe('request failed (500)');
	});
	it('never says the old guess', () => {
		for (const [st, b] of [[401, ''], [403, ''], [401, '{"error":"x"}']] as const) {
			expect(refusalMessage(st, b).includes('auth token')).toBe(false);
		}
	});
	it('knows an elevation refusal and a standby referral by their fields', () => {
		expect(wantsElevation(401, '{"error":"x","elevate":true}')).toBe(true);
		expect(wantsElevation(401, '{"error":"x"}')).toBe(false);
		expect(wantsElevation(403, '{"elevate":true}')).toBe(false);
		expect(standbyLeader(503, '{"standby":true,"leader":"https://b"}')).toBe('https://b');
		expect(standbyLeader(503, '{"standby":true}')).toBe('');
		expect(standbyLeader(503, '{"error":"no majority"}')).toBeNull();
		expect(standbyLeader(502, '{"standby":true}')).toBeNull();
	});
	it("a substitute refused by role is refused, not 'outcome unknown', from an older controller too", () => {
		expect(classify(401, '{"error":"sign in"}', null).kind).toBe('forbidden');
		expect(classify(403, '{"error":"not your role"}', null)).toEqual({ kind: 'forbidden', message: 'not your role' });
		expect(classify(503, 'outcome unknown', 'unknown').kind).toBe('unknown');
	});
});

describe('the leader a referral may open', () => {
	it("is the controller's named leader, at its root", () => {
		expect(leaderTarget('https://plc-b.example:8443', PAGE)).toBe('https://plc-b.example:8443/');
		expect(leaderTarget('https://plc-b.example/', PAGE)).toBe('https://plc-b.example/');
		expect(leaderTarget('http://10.0.0.8:8086', 'http://10.0.0.7:8086/x')).toBe('http://10.0.0.8:8086/');
		expect(leaderTarget('https://plc-b.example', 'http://10.0.0.7/')).toBe('https://plc-b.example/');
	});
	it('drops any path the URL carries (nothing but the root is opened)', () => {
		expect(leaderTarget('https://plc-b.example/evil/path?next=//x', PAGE)).toBe('https://plc-b.example/');
	});
	it('refuses anything hostile', () => {
		for (const bad of [
			'javascript:alert(1)',
			'data:text/html,<script>1</script>',
			'file:///etc/passwd',
			'//evil.example',
			'/elsewhere',
			'evil.example',
			'https://user:pw@plc-b.example',
			'https://user@plc-b.example',
			'ftp://plc-b.example',
			'',
			'not a url'
		]) {
			if (leaderTarget(bad, PAGE) !== null) throw new Error(`followed ${JSON.stringify(bad)}`);
		}
		// From a plain-http page too, where no downgrade rule stands in front.
		for (const bad of ['javascript:alert(1)', 'data:text/html,x', 'file:///etc/passwd', 'ftp://plc-b.example', 'ws://plc-b.example']) {
			let got: string | null = 'threw';
			try {
				got = leaderTarget(bad, 'http://10.0.0.7:8086/');
			} catch {
				/* a throw is a failure too */
			}
			if (got !== null) throw new Error(`from http, followed ${JSON.stringify(bad)}: ${got}`);
		}
		expect(leaderTarget(null, PAGE)).toBeNull();
		expect(leaderTarget(undefined, PAGE)).toBeNull();
	});
	it('never downgrades an https page to http, and never "follows" to itself', () => {
		expect(leaderTarget('http://plc-b.example', PAGE)).toBeNull();
		expect(leaderTarget('https://plc-a.example', PAGE)).toBeNull();
	});
	it('the loop guard: no second follow within the guard', () => {
		const m = new Map<string, string>();
		const st = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) };
		expect(mayFollow(st, 1000)).toBe(true);
		m.set(FOLLOW_KEY, '1000');
		expect(mayFollow(st, 1000 + FOLLOW_GUARD_MS - 1)).toBe(false);
		expect(mayFollow(st, 1000 + FOLLOW_GUARD_MS)).toBe(true);
		expect(mayFollow(st, 999)).toBe(true); // a clock set back is not a reason to stick
		const blocked = { getItem: () => { throw new Error('blocked'); }, setItem: () => {} };
		expect(mayFollow(blocked, 5)).toBe(true);
	});
	it('the sign-in page comes back here', () => {
		expect(signInHref({ pathname: '/screens/pump', search: '?x=1' })).toBe(SIGNIN);
	});
});

describe('elevation: the prompt and one retry', () => {
	it('asks once, proves the password, and repeats the request byte for byte', async () => {
		const w = world({
			'/api/substitutes': (_u, _i, n) => (n === 0 ? elevate401() : json({ started: true })),
			'/api/elevate': () => json({ user: 'akhan', elevatedUntil: '2026-11-20T09:15:00Z' }),
			'/api/me': () => json(signedIn())
		}, ['pw']);
		await w.s.refresh();
		const res = await w.s.fetch('/api/substitutes', WRITE);
		expect(res.status).toBe(200);
		expect(w.prompts.length).toBe(1);
		expect(w.prompts[0].user).toBe('akhan');
		const subs = w.to('/api/substitutes');
		expect(subs.length).toBe(2);
		expect(subs[1].body).toBe(subs[0].body); // the requestId is the same one
		expect(subs[1].method).toBe('POST');
		expect(JSON.parse(w.to('/api/elevate')[0].body ?? '{}')).toEqual({ password: 'pw' });
	});
	it('retries once only: a second elevation refusal is the answer', async () => {
		const w = world({
			'/api/tags': () => elevate401(),
			'/api/elevate': () => json({}),
			'/api/me': () => json(signedIn())
		}, ['pw', 'pw', 'pw']);
		const res = await w.s.fetch('/api/tags', WRITE);
		expect(res.status).toBe(401);
		expect(w.to('/api/tags').length).toBe(2);
		expect(w.prompts.length).toBe(1);
	});
	it('a Request is retried from its copy (its body reads once)', async () => {
		const w = world({
			'/api/tags': (_u, _i, n) => (n === 0 ? elevate401() : json({})),
			'/api/elevate': () => json({}),
			'/api/me': () => json(signedIn())
		}, ['pw']);
		const res = await w.s.fetch(new Request('https://plc-a.example/api/tags', { method: 'POST', body: 'abc' }));
		expect(res.status).toBe(200);
		expect(w.to('/api/tags').length).toBe(2);
	});
	it('cancel: the caller gets the original refusal, and no password is sent', async () => {
		const w = world({ '/api/tags': () => elevate401(), '/api/me': () => json(signedIn()) }, [null]);
		const res = await w.s.fetch('/api/tags', WRITE);
		expect(res.status).toBe(401);
		expect(refusalMessage(res.status, await res.text())).toBe('confirm your password to do this: sign in again (/signin)');
		expect(w.to('/api/elevate').length).toBe(0);
		expect(w.to('/api/tags').length).toBe(1);
	});
	it('concurrent refusals share one prompt, and each is retried once', async () => {
		let release!: (pw: string) => void;
		const typed = new Promise<string | null>((r) => (release = r));
		const w = world({
			'/api/forces': (_u, _i, n) => (n < 2 ? elevate401() : json({})),
			'/api/elevate': () => json({}),
			'/api/me': () => json(signedIn())
		}, [], { prompt: () => typed });
		const a = w.s.fetch('/api/forces', WRITE);
		const b = w.s.fetch('/api/forces', WRITE);
		await new Promise((r) => setTimeout(r, 0));
		release('pw');
		const [ra, rb] = await Promise.all([a, b]);
		expect(w.prompts.length).toBe(1);
		expect(w.to('/api/elevate').length).toBe(1);
		expect(ra.status).toBe(200);
		expect(rb.status).toBe(200);
		expect(w.to('/api/forces').length).toBe(4);
	});
	it('a wrong password asks again, saying why; the right one then proceeds', async () => {
		const w = world({
			'/api/tags': (_u, _i, n) => (n === 0 ? elevate401() : json({})),
			'/api/elevate': (_u, init) =>
				JSON.parse(String(init?.body)).password === 'right' ? json({}) : json({ error: 're-authentication refused: the password is wrong' }, 401),
			'/api/me': () => json(signedIn())
		}, ['wrong', 'right']);
		await w.s.refresh();
		const res = await w.s.fetch('/api/tags', WRITE);
		expect(res.status).toBe(200);
		expect(w.prompts.length).toBe(2);
		expect(w.prompts[1].error).toBe('re-authentication refused: the password is wrong');
		expect(w.nav).toEqual([]);
	});
	it('the fifth wrong password ends the sign-in: to the sign-in page, no retry', async () => {
		let ended = false;
		const w = world({
			'/api/tags': () => elevate401(),
			'/api/elevate': () => {
				ended = true;
				return json({ error: 'refused; this sign-in has ended: sign in again' }, 401);
			},
			'/api/me': () => json(ended ? anonymous() : signedIn())
		}, ['wrong']);
		await w.s.refresh();
		const res = await w.s.fetch('/api/tags', WRITE);
		expect(res.status).toBe(401);
		expect(w.nav).toEqual([SIGNIN]);
		expect(w.to('/api/tags').length).toBe(1);
	});
	it('a refusal a password cannot help (403: not over TLS) stops asking', async () => {
		const w = world({
			'/api/tags': () => elevate401(),
			'/api/elevate': () => json({ error: 'a password needs HTTPS' }, 403),
			'/api/me': () => json(signedIn())
		}, ['pw', 'pw']);
		const res = await w.s.fetch('/api/tags', WRITE);
		expect(res.status).toBe(401);
		expect(w.prompts.length).toBe(1);
		expect(w.to('/api/tags').length).toBe(1);
	});
	it('throttled: asks again with the reason', async () => {
		const w = world({
			'/api/tags': (_u, _i, n) => (n === 0 ? elevate401() : json({})),
			'/api/elevate': (_u, _i, n) => (n === 0 ? json({ error: 'too many attempts: try again in 3 s' }, 429) : json({})),
			'/api/me': () => json(signedIn())
		}, ['pw', 'pw']);
		expect((await w.s.fetch('/api/tags', WRITE)).status).toBe(200);
		expect(w.prompts[1].error).toBe('too many attempts: try again in 3 s');
	});
	it('a cross-origin request is never prompted for (/api/elevate is same-origin only)', async () => {
		const w = world({ 'http://localhost:8086/api/tags': () => elevate401() }, ['pw']);
		const res = await w.s.fetch('http://localhost:8086/api/tags', WRITE);
		expect(res.status).toBe(401);
		expect(w.prompts.length).toBe(0);
		expect(w.sent.length).toBe(1);
	});
});

describe('a mid-session sign-out', () => {
	it('a 401 after a sign-in that /api/me says has ended goes to the sign-in page, coming back here', async () => {
		let gone = false;
		const w = world({
			'/api/me': () => json(gone ? anonymous() : signedIn()),
			'/api/tags': () => json({ error: 'sign in' }, 401)
		});
		await w.s.refresh();
		gone = true;
		await w.s.fetch('/api/tags', WRITE);
		await new Promise((r) => setTimeout(r, 0));
		expect(w.nav).toEqual([SIGNIN]);
	});
	it('decided by /api/me, not the 401: a 401 for a sign-in that still stands goes nowhere', async () => {
		const w = world({ '/api/me': () => json(signedIn()), '/api/tags': () => json({ error: 'invalid credential' }, 401) });
		await w.s.refresh();
		await w.s.fetch('/api/tags', WRITE);
		await new Promise((r) => setTimeout(r, 0));
		expect(w.nav).toEqual([]);
		expect(w.to('/api/me').length).toBe(2);
	});
	it('never signed in: a 401 is just the refusal (no page sends a viewer away)', async () => {
		const w = world({ '/api/me': () => json(anonymous()), '/api/tags': () => json({ error: 'sign in' }, 401) });
		await w.s.refresh();
		await w.s.fetch('/api/tags', WRITE);
		await new Promise((r) => setTimeout(r, 0));
		expect(w.nav).toEqual([]);
	});
	it('a controller without sign-in (a facade) has no page to send anyone to', async () => {
		let gone = false;
		const w = world({ '/api/me': () => json(gone ? anonymous({ signIn: false }) : signedIn()), '/api/tags': () => json({}, 401) });
		await w.s.refresh();
		gone = true;
		await w.s.checkSignedOut();
		expect(w.nav).toEqual([]);
	});
	it('a stream that drops asks /api/me, backing off while it stays down', async () => {
		let gone = false;
		const w = world({ '/api/me': () => json(gone ? anonymous() : signedIn()) });
		await w.s.refresh();
		w.s.streamTrouble();
		w.s.streamTrouble(); // within the wait: no second probe
		await new Promise((r) => setTimeout(r, 0));
		expect(w.to('/api/me').length).toBe(2);
		w.tick(PROBE_MIN_MS);
		w.s.streamTrouble();
		await new Promise((r) => setTimeout(r, 0));
		expect(w.to('/api/me').length).toBe(3);
		w.tick(PROBE_MIN_MS); // the wait has doubled: still too soon
		w.s.streamTrouble();
		expect(w.to('/api/me').length).toBe(3);
		w.s.streamFine(); // reopened: the next drop asks at once
		gone = true;
		w.s.streamTrouble();
		await new Promise((r) => setTimeout(r, 0));
		expect(w.to('/api/me').length).toBe(4);
		expect(w.nav).toEqual([SIGNIN]);
	});
});

describe("a standby's referral", () => {
	const referral = (leader?: string, headers: Record<string, string> = {}) =>
		json({ error: 'this controller is the standby; open the leader', standby: true, ...(leader !== undefined ? { leader } : {}) }, 503, headers);

	it('sends the page to the leader the controller names, once; the write is not re-sent', async () => {
		const w = world({ '/api/tags': () => referral('https://plc-b.example') });
		const res = await w.s.fetch('/api/tags', WRITE);
		expect(res.status).toBe(503);
		expect(w.nav).toEqual(['https://plc-b.example/']);
		expect(w.sent.length).toBe(1);
		expect(w.store.get(FOLLOW_KEY)).toBe('1000000');
	});
	it('a second referral within the guard is shown, not followed (two boxes naming each other)', async () => {
		const storage = new Map<string, string>([[FOLLOW_KEY, String(1_000_000 - 5_000)]]);
		const w = world({ '/api/state': () => referral('https://plc-b.example') }, [], { storage });
		await w.s.fetch('/api/state');
		expect(w.nav).toEqual([]);
		expect(w.s.referral).toEqual({ leader: 'https://plc-b.example/', message: 'this controller is the standby; open the leader' });
	});
	it('a hostile leader is refused: no navigation, a banner without a link', async () => {
		for (const bad of ['javascript:alert(1)', '//evil.example', 'http://plc-b.example', 'data:text/html,x']) {
			const w = world({ '/api/tags': () => referral(bad) });
			await w.s.fetch('/api/tags', WRITE);
			if (w.nav.length) throw new Error(`followed ${bad}`);
			expect(w.s.referral?.leader).toBeNull();
		}
	});
	it('a Location header is never followed: only the leader the body names', async () => {
		const w = world({ '/api/tags': () => referral(undefined, { Location: 'https://evil.example/' }) });
		await w.s.fetch('/api/tags', WRITE);
		expect(w.nav).toEqual([]);
		expect(w.s.referral).toEqual({ leader: null, message: 'this controller is the standby; open the leader' });
	});
	it('any other 503 (no majority, outcome unknown) is not a referral', async () => {
		const w = world({ '/api/substitutes': () => json({ error: 'no majority' }, 503, { 'X-Nautilus-Outcome': 'refused' }) });
		await w.s.fetch('/api/substitutes', WRITE);
		expect(w.nav).toEqual([]);
		expect(w.s.referral).toBeNull();
	});
	it('a stream on a standby: /api/me is referred, and followed', async () => {
		const w = world({ '/api/me': () => referral('https://plc-b.example') });
		w.s.streamTrouble();
		await new Promise((r) => setTimeout(r, 0));
		expect(w.nav).toEqual(['https://plc-b.example/']);
	});
});

describe('a controller without auth (or without /api/me)', () => {
	it('a 404 on /api/me: the kit does nothing, an elevation-looking 401 included', async () => {
		const w = world({ '/api/me': () => new Response('404 page not found', { status: 404 }), '/api/tags': () => elevate401() }, ['pw']);
		expect(await w.s.refresh()).toBeNull();
		expect(w.s.supported).toBe(false);
		const res = await w.s.fetch('/api/tags', WRITE);
		expect(res.status).toBe(401);
		expect(w.prompts.length).toBe(0);
		w.s.streamTrouble();
		expect(w.to('/api/me').length).toBe(1);
	});
	it('a success passes straight through, untouched', async () => {
		const w = world({ '/api/tags': () => json({ ok: 1 }) });
		const res = await w.s.fetch('/api/tags', WRITE);
		expect(await res.json()).toEqual({ ok: 1 });
		expect(w.sent.length).toBe(1);
	});
});

describe('badging an elevated control ahead of time', () => {
	const at = (iso: string) => Date.parse(iso);
	it("judged by the controller's clock, not the browser's", () => {
		// Elevated until 09:15 by the controller, whose clock says 09:00.
		const me = signedIn({ session: { expires: '2026-11-20T20:00:00Z', elevatedUntil: '2026-11-20T09:15:00Z' }, now: '2026-11-20T09:00:00Z' });
		// The browser's clock runs an hour fast: it says 10:00.
		const local = at('2026-11-20T10:00:00Z');
		const skew = clockSkew(me, local);
		expect(skew).toBe(-3_600_000);
		expect(needsPassword(me, 'force', local, skew)).toBe(false);
		expect(needsPassword(me, 'force', local + 16 * 60_000, skew)).toBe(true);
		// Without the skew the fast browser would badge it already.
		expect(needsPassword(me, 'force', local)).toBe(true);
	});
	it('only for an elevated permission the user holds, and never for a token', () => {
		const me = signedIn();
		const now = at('2026-11-20T09:00:00Z');
		expect(needsPassword(me, 'force', now)).toBe(true);
		expect(needsPassword(me, 'write', now)).toBe(false); // not elevated
		expect(needsPassword(me, 'edit', now)).toBe(false); // elevated, not held
		expect(needsPassword(signedIn({ session: undefined, principal: 'token' }), 'force', now)).toBe(false);
		expect(needsPassword(null, 'force', now)).toBe(false);
	});
});

describe('every kit client goes through the kit fetch', () => {
	// One spec, not several: specs run concurrently, and these swap the one
	// installed fetch (and the browser's).
	it('the browser fetch until one is installed; then substitutes, IO-Link parameters and history use it', async () => {
		const seen: string[] = [];
		const g = globalThis as { fetch: typeof fetch };
		const browser = g.fetch;
		g.fetch = (async (i: RequestInfo | URL) => (seen.push('browser:' + String(i)), new Response('b'))) as typeof fetch;
		try {
			await kitFetch('/a');
			setKitFetch(async (i) => {
				seen.push(String(i));
				return json({ substitutes: [], rules: [], params: [], series: {} });
			});
			await kitFetch('/b');
			await new SubstituteClient().list();
			await new PortParamClient().list('X1');
			await fetchHistory(['T1'], 0, 1000);
			setKitFetch(null);
			await kitFetch('/c');
		} finally {
			g.fetch = browser;
			setKitFetch(null);
		}
		expect(seen.length).toBe(6);
		expect([seen[0], seen[1], seen[5]]).toEqual(['browser:/a', '/b', 'browser:/c']);
		seen.splice(0, 2);
		expect(seen[0].startsWith('/api/substitutes')).toBe(true);
		expect(seen[1].startsWith('/api/iolink/X1/params')).toBe(true);
		expect(seen[2].startsWith('/api/history')).toBe(true);
	});
	// The rune files cannot run on plain node; read them instead.
	it('no kit source calls the browser fetch directly', () => {
		const files = [
			'alarms.svelte.ts',
			'realtime.svelte.ts',
			'history.ts',
			'iolink.ts',
			'substitutes.ts',
			'taghistory.svelte.ts',
			'trend.svelte.ts'
		];
		for (const f of files) {
			const src = readFileSync('src/lib/' + f, 'utf8');
			const bare = src.split('\n').filter((l) => /(^|[^A-Za-z#.])fetch\(/.test(l) && !l.trim().startsWith('//') && !l.trim().startsWith('*'));
			if (bare.length) throw new Error(`${f} calls fetch( directly: ${bare.join(' | ')}`);
		}
	});
	it("the realtime client reports its stream to the kit, and writeTag shows the controller's reason", () => {
		const src = readFileSync('src/lib/realtime.svelte.ts', 'utf8');
		const open = src.slice(src.indexOf('es.onopen'), src.indexOf('es.onerror'));
		const error = src.slice(src.indexOf('es.onerror'), src.indexOf('es.onmessage'));
		expect(open.includes('reportStreamFine()')).toBe(true);
		expect(error.includes('reportStreamTrouble()')).toBe(true);
		const write = src.slice(src.indexOf('async writeTag('));
		expect(write.includes('kitFetch(')).toBe(true);
		expect(write.includes('refusalMessage(res.status')).toBe(true);
		expect(write.includes('auth token')).toBe(false);
	});
});

/** A response as the browser hands it after following a redirect: its type and final url. */
function redirected(res: Response, url: string, type: ResponseType = 'cors'): Response {
	Object.defineProperty(res, 'url', { value: url });
	Object.defineProperty(res, 'type', { value: type });
	return res;
}

describe('a redirected answer (L1, #1090 review)', () => {
	it('knows a response from another origin', () => {
		expect(sameOriginResponse(redirected(json({}), 'https://evil.example/x'), PAGE)).toBe(false);
		expect(sameOriginResponse(redirected(json({}), 'https://plc-a.example/api/tags', 'basic'), PAGE)).toBe(true);
		expect(sameOriginResponse(redirected(json({}), 'https://plc-a.example/x', 'default'), PAGE)).toBe(true);
		expect(sameOriginResponse(redirected(json({}), 'https://evil.example/x', 'default'), PAGE)).toBe(false);
		expect(sameOriginResponse(redirected(json({}), '', 'opaque'), PAGE)).toBe(false);
		expect(sameOriginResponse(json({}), PAGE)).toBe(true); // built, no url: judged by its type
	});
	it("a same-origin request redirected to another origin's referral is not followed", async () => {
		const w = world({
			'/api/tags': () => redirected(json({ error: 'standby', standby: true, leader: 'https://evil.example' }, 503), 'https://evil.example/api/tags')
		});
		await w.s.fetch('/api/tags', WRITE);
		expect(w.nav).toEqual([]);
		expect(w.s.referral).toBeNull();
	});
	it("nor is another origin's elevation refusal prompted for", async () => {
		const w = world({ '/api/tags': () => redirected(elevate401(), 'https://evil.example/api/tags') }, ['pw']);
		const res = await w.s.fetch('/api/tags', WRITE);
		expect(res.status).toBe(401);
		expect(w.prompts.length).toBe(0);
		expect(w.sent.length).toBe(1);
	});
	it('nor a redirected /api/me referral', async () => {
		const w = world({ '/api/me': () => redirected(json({ error: 'standby', standby: true, leader: 'https://evil.example' }, 503), 'https://evil.example/api/me') });
		await w.s.refresh();
		expect(w.nav).toEqual([]);
	});
});

describe('a late elevation refusal (L3, #1090 review)', () => {
	it('is retried without asking when /api/me says the password is fresh by the controller clock', async () => {
		const fresh = signedIn({ session: { expires: '2026-11-20T20:00:00Z', elevatedUntil: '2026-11-20T09:14:00Z' }, now: '2026-11-20T09:00:00Z' });
		const w = world({
			'/api/forces': (_u, _i, n) => (n === 0 ? elevate401() : json({})),
			'/api/me': () => json(fresh)
		}, ['pw']);
		const res = await w.s.fetch('/api/forces', WRITE);
		expect(res.status).toBe(200);
		expect(w.prompts.length).toBe(0);
		expect(w.to('/api/elevate').length).toBe(0);
		expect(w.to('/api/forces').length).toBe(2);
	});
	it('a refusal while another prompt is open shares it; a cancel there asks nothing more', async () => {
		let release!: (pw: string | null) => void;
		const typed = new Promise<string | null>((r) => (release = r));
		// A second /api/me would be slow: answered only after the cancel, so a
		// request that asked it (instead of waiting on the open prompt) would
		// find no prompt open any more and ask again.
		let late!: () => void;
		const after = new Promise<void>((r) => (late = r));
		const w = world({
			'/api/forces': () => elevate401(),
			'/api/me': async (_u, _i, n) => {
				if (n > 0) await after;
				return json(signedIn());
			}
		}, [], { prompt: () => typed });
		const a = w.s.fetch('/api/forces', WRITE);
		await new Promise((r) => setTimeout(r, 0));
		const b = w.s.fetch('/api/forces', WRITE);
		await new Promise((r) => setTimeout(r, 0));
		release(null);
		await new Promise((r) => setTimeout(r, 0));
		late();
		const [ra, rb] = await Promise.all([a, b]);
		expect(ra.status).toBe(401);
		expect(rb.status).toBe(401);
		expect(w.prompts.length).toBe(1);
		expect(w.to('/api/forces').length).toBe(2);
	});
});

describe('the loop guard without sessionStorage (L5, #1090 review)', () => {
	const blocked = {
		getItem: (): string | null => {
			throw new Error('blocked');
		},
		setItem: () => {
			throw new Error('blocked');
		}
	};
	it('falls back to memory: one follow, then none within the guard', () => {
		const mem = { at: NaN };
		expect(mayFollow(blocked, 1000, mem)).toBe(true);
		markFollowed(blocked, 1000, mem);
		expect(mem.at).toBe(1000);
		expect(mayFollow(blocked, 1000 + FOLLOW_GUARD_MS - 1, mem)).toBe(false);
		expect(mayFollow(blocked, 1000 + FOLLOW_GUARD_MS, mem)).toBe(true);
		const none = { at: NaN };
		markFollowed(null, 50, none);
		expect(mayFollow(null, 60, none)).toBe(false);
	});
	it('storage that works is the guard; memory is not consulted', () => {
		const m = new Map<string, string>();
		const st = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) };
		const mem = { at: 1000 };
		expect(mayFollow(st, 1001, mem)).toBe(true);
		markFollowed(st, 2000, mem);
		expect(m.get(FOLLOW_KEY)).toBe('2000');
		expect(mem.at).toBe(1000);
	});
});
