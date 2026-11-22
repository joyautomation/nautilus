// The Web Push card's contract (#722): why it cannot, never asking for the
// permission before it is sure the controller can page, and never leaving a
// browser subscribed to a pager that did not take it.
import { describe, it, expect } from './harness.js';
import {
	PushClient,
	pushState,
	readPushEnv,
	enablePush,
	disablePush,
	keyBytes,
	endpointId,
	realPushBrowser,
	type PushEnv,
	type PushBrowser,
	type PushSub
} from '../src/lib/push.js';

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status });
const text = (b: string, status: number) => new Response(b, { status });

type Sent = { url: string; method: string; body: unknown; headers: Record<string, string> };
function fake(answers: Array<Response | Error>, token?: string) {
	const sent: Sent[] = [];
	const f = (async (url: string, init?: RequestInit) => {
		sent.push({
			url,
			method: init?.method ?? 'GET',
			body: init?.body ? JSON.parse(String(init.body)) : undefined,
			headers: (init?.headers as Record<string, string>) ?? {}
		});
		const a = answers.shift();
		if (!a) throw new Error('no more answers');
		if (a instanceof Error) throw a;
		return a;
	}) as unknown as typeof fetch;
	return { sent, client: new PushClient({ fetch: f, token }) };
}

const goodEnv: PushEnv = { secure: true, serviceWorker: true, pushManager: true, notification: true, permission: 'default', standalone: false, ios: false };
const on = { configured: true, signedIn: true, subscribed: false };

describe('pushState — the first cause a person can fix', () => {
	it('plain http says https is needed, before anything else', () => {
		const s = pushState({ ...goodEnv, secure: false, serviceWorker: false, ios: true }, { signedIn: false, configured: false, subscribed: false });
		expect(s.kind).toBe('insecure');
		expect(s.canEnable).toBe(false);
		expect(s.message.includes('https')).toBe(true);
	});
	it('an iPhone not on the Home Screen is told to add it, even over https', () => {
		const s = pushState({ ...goodEnv, ios: true, standalone: false }, on);
		expect(s.kind).toBe('ios-install');
		expect(s.message.includes('Home Screen')).toBe(true);
		expect(s.canEnable).toBe(false);
	});
	it('an iPhone on the Home Screen can enable', () => {
		expect(pushState({ ...goodEnv, ios: true, standalone: true }, on).canEnable).toBe(true);
	});
	it('a browser with no push says so', () => {
		for (const k of ['serviceWorker', 'pushManager', 'notification'] as const) {
			expect(pushState({ ...goodEnv, [k]: false }, on).kind).toBe('unsupported');
		}
	});
	it('signed out, no push on the controller, denied, off, on', () => {
		expect(pushState(goodEnv, { ...on, signedIn: false }).kind).toBe('signed-out');
		expect(pushState(goodEnv, { ...on, configured: false }).kind).toBe('not-set-up');
		expect(pushState({ ...goodEnv, permission: 'denied' }, on).kind).toBe('denied');
		const off = pushState(goodEnv, on);
		expect(off.kind).toBe('off');
		expect(off.canEnable).toBe(true);
		const live = pushState({ ...goodEnv, permission: 'granted' }, { ...on, subscribed: true });
		expect(live.kind).toBe('on');
		expect(live.canEnable).toBe(false);
	});
	it('a subscription whose permission was since withdrawn is not "on"', () => {
		expect(pushState({ ...goodEnv, permission: 'denied' }, { ...on, subscribed: true }).kind).toBe('denied');
	});
	it('a controller with no sign-in at all says so, not "sign in"', () => {
		const s = pushState(goodEnv, { ...on, hasAuth: false, signedIn: false });
		expect(s.kind).toBe('no-auth');
		expect(s.canEnable).toBe(false);
	});
	it('unknown server facts do not block enabling', () => {
		expect(pushState(goodEnv, { subscribed: false }).canEnable).toBe(true);
	});
});

describe('readPushEnv', () => {
	it('reads a secure Chromium', () => {
		const e = readPushEnv({ isSecureContext: true, navigator: { userAgent: 'Mozilla Chrome', serviceWorker: {} }, PushManager: {}, Notification: { permission: 'granted' }, matchMedia: () => ({ matches: false }) });
		expect(e.secure && e.serviceWorker && e.pushManager && e.notification).toBe(true);
		expect(e.permission).toBe('granted');
		expect(e.ios).toBe(false);
	});
	it('knows an iPhone, an iPad posing as a Mac, and the Home Screen', () => {
		const base = { isSecureContext: true, PushManager: {}, Notification: { permission: 'default' }, matchMedia: () => ({ matches: false }) };
		expect(readPushEnv({ ...base, navigator: { userAgent: 'iPhone', serviceWorker: {} } }).ios).toBe(true);
		expect(readPushEnv({ ...base, navigator: { userAgent: 'Macintosh', maxTouchPoints: 5, serviceWorker: {} } }).ios).toBe(true);
		expect(readPushEnv({ ...base, navigator: { userAgent: 'Macintosh', maxTouchPoints: 0, serviceWorker: {} } }).ios).toBe(false);
		expect(readPushEnv({ ...base, navigator: { userAgent: 'iPhone', standalone: true, serviceWorker: {} } }).standalone).toBe(true);
		expect(readPushEnv({ ...base, matchMedia: () => ({ matches: true }), navigator: { userAgent: 'x' } }).standalone).toBe(true);
	});
	it('a page with nothing is insecure and unsupported, not an exception', () => {
		const e = readPushEnv({});
		expect(e.secure || e.serviceWorker || e.pushManager || e.notification).toBe(false);
		expect(e.permission).toBe(null);
	});
});

describe('PushClient', () => {
	it('reads the key and the list', async () => {
		const { client, sent } = fake([json({ publicKey: 'BKEY' }), json({ subscriptions: [{ id: 'a', host: 'h', at: 't' }] })], 'tok');
		const k = await client.key();
		expect(k.ok && k.value).toBe('BKEY');
		const l = await client.list();
		expect(l.ok && l.value.length).toBe(1);
		expect(sent[0].url).toBe('/api/push/key');
		expect(sent[1].url).toBe('/api/push/subscriptions');
		expect(sent[0].headers['X-Nautilus-Token']).toBe('tok');
	});
	it('says a controller refusal in its own words', async () => {
		const { client } = fake([json({ error: 'push pages a person: sign in as a user' }, 403), text('', 401), new Error('down'), text('not json', 200)]);
		const a = await client.key();
		expect(!a.ok && a.message).toBe('push pages a person: sign in as a user');
		const b = await client.list();
		expect(!b.ok && b.status).toBe(401);
		const c = await client.key();
		expect(!c.ok && c.message).toBe('controller unreachable');
		const d = await client.key();
		expect(!d.ok && d.message.includes('not JSON')).toBe(true);
	});
	it('an answer it does not understand is not a success', async () => {
		const { client } = fake([json({}), json({ nope: 1 }), json({ id: 7 })]);
		expect((await client.key()).ok).toBe(false);
		expect((await client.list()).ok).toBe(false);
		expect((await client.subscribe({})).ok).toBe(false);
	});
	it('subscribes with the browser JSON as it is, and removes by id or endpoint', async () => {
		const { client, sent } = fake([json({ id: 'x1', host: 'h', at: 't' }, 201), new Response(null, { status: 204 }), new Response(null, { status: 204 })]);
		const sub = { endpoint: 'https://p/e', expirationTime: null, keys: { p256dh: 'A', auth: 'B' } };
		expect((await client.subscribe(sub)).ok).toBe(true);
		expect(sent[0].method).toBe('POST');
		expect(sent[0].body).toEqual(sub);
		expect((await client.remove({ id: 'x1' })).ok).toBe(true);
		expect((await client.remove({ endpoint: 'https://p/e' })).ok).toBe(true);
		expect(sent[1].method).toBe('DELETE');
		expect(sent[1].body).toEqual({ id: 'x1' });
		expect(sent[2].body).toEqual({ endpoint: 'https://p/e' });
	});
});

// a browser that records what it was asked, in order
function browser(opts: { permission?: NotificationPermission; existing?: boolean; otherKey?: boolean; subscribeFails?: boolean; permissionThrows?: boolean } = {}) {
	const log: string[] = [];
	const sub: PushSub & { dropped: boolean } = {
		endpoint: 'https://push.example.net/p/1',
		dropped: false,
		toJSON: () => ({ endpoint: 'https://push.example.net/p/1', keys: { p256dh: 'P', auth: 'A' } }),
		unsubscribe: async () => {
			sub.dropped = true;
			log.push('unsubscribe-new');
			return true;
		}
	};
	const old: PushSub = { endpoint: 'https://old/e', toJSON: () => ({}), unsubscribe: async () => { log.push('unsubscribe-old'); current = null; return true; } };
	let current: PushSub | null = opts.existing ? old : null;
	const b: PushBrowser = {
		requestPermission: async () => {
			log.push('permission');
			if (opts.permissionThrows) throw new Error('no prompt');
			return opts.permission ?? 'granted';
		},
		registration: async () => ({
			pushManager: {
				getSubscription: async () => current,
				subscribe: async (o) => {
					log.push('subscribe:' + o.applicationServerKey.length);
					if (opts.subscribeFails) throw new Error('push service error');
					// a subscription under another key refuses a new one until it is dropped
					if (opts.existing && opts.otherKey && current === old) {
						const e = new Error('different key');
						e.name = 'InvalidStateError';
						throw e;
					}
					current = sub;
					return sub;
				}
			}
		})
	};
	return { b, log, sub };
}

describe('enablePush', () => {
	it('asks the controller for its key before the permission, then subscribes, then tells the controller', async () => {
		const { client, sent } = fake([json({ publicKey: 'AQID' }), json({ id: 'x1', host: 'push.example.net', at: 't' }, 201)]);
		const { b, log } = browser();
		const r = await enablePush(client, b);
		expect(r.ok).toBe(true);
		expect(log.join(',')).toBe('permission,subscribe:3');
		expect(sent.map((s) => s.url).join(',')).toBe('/api/push/key,/api/push/subscribe');
		expect(sent[1].body).toEqual({ endpoint: 'https://push.example.net/p/1', keys: { p256dh: 'P', auth: 'A' } });
	});
	it('a controller with no push never causes a permission prompt', async () => {
		const { client } = fake([json({ error: 'push is not set up' }, 404)]);
		const { b, log } = browser();
		const r = await enablePush(client, b);
		expect(!r.ok && r.why).toBe('controller');
		expect(log.length).toBe(0);
	});
	it('a refused or dismissed permission subscribes nothing', async () => {
		for (const [p, why] of [['denied', 'denied'], ['default', 'dismissed']] as const) {
			const { client, sent } = fake([json({ publicKey: 'AQID' })]);
			const { b, log } = browser({ permission: p });
			const r = await enablePush(client, b);
			expect(!r.ok && r.why).toBe(why);
			expect(log.join(',')).toBe('permission');
			expect(sent.length).toBe(1);
		}
	});
	it('drops a subscription made under another key, once, and subscribes again', async () => {
		const { client } = fake([json({ publicKey: 'AQID' }), json({ id: 'x', host: 'h', at: 't' }, 201)]);
		const { b, log } = browser({ existing: true, otherKey: true });
		const r = await enablePush(client, b);
		expect(r.ok).toBe(true);
		expect(log.join(',')).toBe('permission,subscribe:3,unsubscribe-old,subscribe:3');
	});
	it('keeps a subscription made under the same key (no orphaned endpoint)', async () => {
		const { client } = fake([json({ publicKey: 'AQID' }), json({ id: 'x', host: 'h', at: 't' }, 201)]);
		const { b, log } = browser({ existing: true });
		await enablePush(client, b);
		expect(log.join(',')).toBe('permission,subscribe:3');
	});
	it('a subscribe that fails for another reason never costs the working subscription', async () => {
		const { client } = fake([json({ publicKey: 'AQID' })]);
		const { b, log } = browser({ existing: true, subscribeFails: true });
		const r = await enablePush(client, b);
		expect(!r.ok && r.why).toBe('browser');
		expect(log.join(',')).toBe('permission,subscribe:3');
	});
	it('a malformed key from the controller is refused before any prompt', async () => {
		const { client } = fake([json({ publicKey: 'A' })]);
		const { b, log } = browser();
		const r = await enablePush(client, b);
		expect(!r.ok && r.why).toBe('controller');
		expect(log.length).toBe(0);
	});
	it('a subscription the controller refuses is dropped from the browser again', async () => {
		const { client } = fake([json({ publicKey: 'AQID' }), json({ error: 'the subscription could not be saved' }, 503)]);
		const { b, sub } = browser();
		const r = await enablePush(client, b);
		expect(!r.ok && r.why).toBe('controller');
		expect(!r.ok && r.message).toBe('the subscription could not be saved');
		expect(sub.dropped).toBe(true);
	});
	it('a browser that cannot subscribe or ask is a browser failure, nothing sent', async () => {
		for (const o of [{ subscribeFails: true }, { permissionThrows: true }]) {
			const { client, sent } = fake([json({ publicKey: 'AQID' })]);
			const { b } = browser(o);
			const r = await enablePush(client, b);
			expect(!r.ok && r.why).toBe('browser');
			expect(sent.length).toBe(1);
		}
	});
});

describe('disablePush', () => {
	it('takes the controller off first, then the browser', async () => {
		const { client, sent } = fake([new Response(null, { status: 204 })]);
		const { b, log } = browser({ existing: true });
		const r = await disablePush(client, b);
		expect(r.ok).toBe(true);
		expect(sent[0].body).toEqual({ endpoint: 'https://old/e' });
		expect(log.join(',')).toBe('unsubscribe-old');
	});
	it('keeps the browser subscription when the controller would not take it off', async () => {
		const { client } = fake([json({ error: 'could not be saved' }, 503)]);
		const { b, log } = browser({ existing: true });
		const r = await disablePush(client, b);
		expect(!r.ok && r.message).toBe('could not be saved');
		expect(log.length).toBe(0);
	});
	it('a controller that never had it is the goal met', async () => {
		const { client } = fake([json({ error: 'you have no such subscription' }, 404)]);
		const { b, log } = browser({ existing: true });
		expect((await disablePush(client, b)).ok).toBe(true);
		expect(log.join(',')).toBe('unsubscribe-old');
	});
	it('a device with no subscription has nothing to do', async () => {
		const { client, sent } = fake([]);
		const { b } = browser();
		expect((await disablePush(client, b)).ok).toBe(true);
		expect(sent.length).toBe(0);
	});
});

describe('keys and ids', () => {
	it('keyBytes decodes unpadded base64url', () => {
		expect(Array.from(keyBytes('AQID'))).toEqual([1, 2, 3]);
		expect(Array.from(keyBytes('-_8'))).toEqual([251, 255]);
		expect(keyBytes('').length).toBe(0);
	});
	it("endpointId is the controller's id for an endpoint", async () => {
		expect(await endpointId('https://push.example.net/p/1')).toBe('d3083df850d73ce85f2c9efde024ca85');
	});
});

describe('realPushBrowser', () => {
	it('a page with no service worker is told so instead of waiting for ever', async () => {
		const never = new Promise(() => {});
		const b = realPushBrowser({ navigator: { serviceWorker: { ready: never } }, Notification: { requestPermission: async () => 'granted' } }, () => Promise.reject(new Error('timeout')));
		let msg = '';
		try {
			await b.registration();
		} catch (e) {
			msg = (e as Error).message;
		}
		expect(msg.includes('no service worker')).toBe(true);
	});
	it('a worker that is ready is used', async () => {
		const reg = { pushManager: {} };
		const b = realPushBrowser({ navigator: { serviceWorker: { ready: Promise.resolve(reg) } } }, () => new Promise<never>(() => {}));
		expect(await b.registration()).toBe(reg as never);
	});
	it('asks the real Notification for the permission', async () => {
		const b = realPushBrowser({ navigator: {}, Notification: { requestPermission: async () => 'granted' } });
		expect(await b.requestPermission()).toBe('granted');
	});
});
