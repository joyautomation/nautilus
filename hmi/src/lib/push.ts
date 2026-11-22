// Web Push for an installed HMI (#722): the client for /api/push/*, the
// browser-side enable and disable, and the pure rules the card renders from.
// No runes, no DOM; tested in tests/push.test.ts with fakes for the fetch
// and the browser, because what it must never do is quiet:
//
//   - subscribe only from a user gesture the caller owns (the card's
//     button), and never ask for the permission before the person asked;
//   - say why it cannot (no https, an iPhone not on the Home Screen, a
//     denied permission, a controller with no push) instead of leaving a
//     button that does nothing;
//   - never keep a browser subscription the controller refused: if the
//     controller does not take it, the browser's is dropped again, so a
//     phone is not left "subscribed" to a pager that does not know it.
//
// The controller binds a subscription to the signed-in person; this sends
// nothing about who. Nothing here reads or shows a subscription's endpoint
// or keys back: the controller never returns them.

import { kitFetch, type Fetch } from './kitfetch.js';
import { refusalMessage } from './auth.js';

/** One of this person's phones, as GET /api/push/subscriptions says it. */
export interface PushDevice {
	id: string;
	/** The push service's host (never the endpoint). */
	host: string;
	at: string;
}

export type PushAnswer<T> = { ok: true; value: T } | { ok: false; status: number; message: string };

export interface PushClientOptions {
	/** Default `/api/push`. */
	url?: string;
	/** `X-Nautilus-Token`, for a cross-origin reader. */
	token?: string;
	/** Default: the kit's fetch. */
	fetch?: Fetch;
}

const UNREACHABLE = 'controller unreachable';

export class PushClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: Fetch;

	constructor(opts: PushClientOptions = {}) {
		this.#url = opts.url ?? '/api/push';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
	}

	#headers(json = false): Record<string, string> {
		const h: Record<string, string> = {};
		if (this.#token) h['X-Nautilus-Token'] = this.#token;
		if (json) h['Content-Type'] = 'application/json';
		return h;
	}

	async #send<T>(path: string, init: RequestInit, pick: (j: unknown) => T | undefined, failed: string): Promise<PushAnswer<T>> {
		let res: Response;
		try {
			res = await this.#fetch(this.#url + path, init);
		} catch {
			return { ok: false, status: 0, message: UNREACHABLE };
		}
		const text = await res.text();
		if (!res.ok) return { ok: false, status: res.status, message: refusalMessage(res.status, text, failed) };
		let j: unknown = undefined;
		if (text) {
			try {
				j = JSON.parse(text);
			} catch {
				return { ok: false, status: res.status, message: 'the controller answered something that is not JSON' };
			}
		}
		const v = pick(j);
		if (v === undefined) return { ok: false, status: res.status, message: 'the controller answered something this page does not understand' };
		return { ok: true, value: v };
	}

	/** The VAPID public key a browser subscribes with. 404: this controller has no push. */
	key(): Promise<PushAnswer<string>> {
		return this.#send('/key', { headers: this.#headers() }, (j) => {
			const k = (j as { publicKey?: unknown } | undefined)?.publicKey;
			return typeof k === 'string' && k ? k : undefined;
		}, 'reading the push key failed');
	}

	/** This person's phones. */
	list(): Promise<PushAnswer<PushDevice[]>> {
		return this.#send('/subscriptions', { headers: this.#headers() }, (j) => {
			const s = (j as { subscriptions?: unknown } | undefined)?.subscriptions;
			return Array.isArray(s) ? (s as PushDevice[]) : undefined;
		}, 'reading the subscriptions failed');
	}

	/** Hand the controller a browser's subscription (`PushSubscription.toJSON()`). */
	subscribe(sub: unknown): Promise<PushAnswer<PushDevice>> {
		return this.#send('/subscribe', { method: 'POST', headers: this.#headers(true), body: JSON.stringify(sub) }, (j) => {
			const d = j as Partial<PushDevice> | undefined;
			return d && typeof d.id === 'string' ? (d as PushDevice) : undefined;
		}, 'subscribing failed');
	}

	/** Take a phone off, by its id or by the browser's endpoint. */
	remove(by: { id: string } | { endpoint: string }): Promise<PushAnswer<true>> {
		return this.#send('/subscribe', { method: 'DELETE', headers: this.#headers(true), body: JSON.stringify(by) }, () => true, 'removing the subscription failed');
	}
}

// ---- what the browser can do

/** What the page can see of its browser. */
export interface PushEnv {
	/** window.isSecureContext: https or localhost. */
	secure: boolean;
	serviceWorker: boolean;
	pushManager: boolean;
	notification: boolean;
	/** Notification.permission, or null where there is no Notification. */
	permission: NotificationPermission | null;
	/** Running from the Home Screen / as an installed app. */
	standalone: boolean;
	ios: boolean;
}

/** Read the environment from the browser (or from a fake global). */
export function readPushEnv(g: Record<string, any> = globalThis as Record<string, any>): PushEnv {
	const nav = g.navigator ?? {};
	const ua = String(nav.userAgent ?? '');
	// iPadOS 13+ says it is a Mac: touch points tell it apart
	const ios = /iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && (nav.maxTouchPoints ?? 0) > 1);
	return {
		secure: g.isSecureContext === true,
		serviceWorker: 'serviceWorker' in nav,
		pushManager: 'PushManager' in g,
		notification: 'Notification' in g,
		permission: 'Notification' in g ? (g.Notification.permission as NotificationPermission) : null,
		standalone: !!(g.matchMedia?.('(display-mode: standalone)')?.matches || nav.standalone === true),
		ios
	};
}

/** What the card must say, and whether its Enable button works. */
export type PushStateKind =
	| 'insecure'
	| 'unsupported'
	| 'ios-install'
	| 'signed-out'
	| 'not-set-up'
	| 'denied'
	| 'off'
	| 'on';

export interface PushState {
	kind: PushStateKind;
	/** One line for the person, with the fix. */
	message: string;
	/** The Enable button works. */
	canEnable: boolean;
}

export interface PushServerFacts {
	/** The controller has push (GET /api/push/key answered); undefined: not asked yet. */
	configured?: boolean;
	/** A person is signed in (a subscription belongs to one); undefined: not known yet. */
	signedIn?: boolean;
	/** This browser holds a subscription the controller knows. */
	subscribed: boolean;
}

/**
 * What the card shows. The first cause that applies wins, in the order a
 * person can fix them: https, the browser, the iPhone's Home Screen, a
 * sign-in, the controller, the permission.
 */
export function pushState(env: PushEnv, server: PushServerFacts): PushState {
	const no = (kind: PushStateKind, message: string): PushState => ({ kind, message, canEnable: false });
	if (!env.secure) {
		return no('insecure', 'Install and notifications need https: this page is on plain http. Open the controller over https (its own TLS, `tailscale serve`, or a reverse proxy).');
	}
	// iOS: web push exists only for an app added to the Home Screen (16.4+)
	if (env.ios && !env.standalone) {
		return no('ios-install', 'On an iPhone or iPad, notifications work only once this page is added to the Home Screen (Share, then Add to Home Screen) and opened from there. iOS 16.4 or later.');
	}
	if (!env.serviceWorker || !env.pushManager || !env.notification) {
		return no('unsupported', 'This browser cannot receive push notifications.');
	}
	if (server.signedIn === false) {
		return no('signed-out', 'Sign in to be paged on this device: a subscription belongs to a person.');
	}
	if (server.configured === false) {
		return no('not-set-up', 'This controller does not send push notifications (alarms.channels.push is not in its manifest).');
	}
	if (server.subscribed && env.permission === 'granted') {
		return { kind: 'on', message: 'This device is paged for the alarms you may read.', canEnable: false };
	}
	if (env.permission === 'denied') {
		return no('denied', "Notifications are blocked for this site. Allow them in the browser's site settings, then come back.");
	}
	return { kind: 'off', message: 'Turn on to be paged on this device.', canEnable: true };
}

// ---- enable and disable

/** The browser parts enable and disable use. A caller passes the real ones; a test, fakes. */
export interface PushBrowser {
	requestPermission(): Promise<NotificationPermission>;
	registration(): Promise<{
		pushManager: {
			getSubscription(): Promise<PushSub | null>;
			subscribe(o: { userVisibleOnly: boolean; applicationServerKey: Uint8Array }): Promise<PushSub>;
		};
	}>;
}

export interface PushSub {
	endpoint: string;
	toJSON(): unknown;
	unsubscribe(): Promise<boolean>;
}

/** The real browser. Throws where there is no service worker. */
export function realPushBrowser(g: Record<string, any> = globalThis as Record<string, any>): PushBrowser {
	return {
		requestPermission: () => Promise.resolve(g.Notification.requestPermission()),
		registration: () => g.navigator.serviceWorker.ready
	};
}

/** applicationServerKey from the controller's unpadded base64url. */
export function keyBytes(b64url: string): Uint8Array {
	const s = b64url.replace(/-/g, '+').replace(/_/g, '/');
	const bin = atob(s + '='.repeat((4 - (s.length % 4)) % 4));
	const out = new Uint8Array(bin.length);
	for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
	return out;
}

export type EnableResult =
	| { ok: true; device: PushDevice }
	| { ok: false; why: 'denied' | 'dismissed' | 'controller' | 'browser'; message: string };

/**
 * Turn push on for this device. Call it from the Enable button's click:
 * the permission prompt needs the gesture. Order: the controller's key
 * first (so a controller without push never causes a prompt), then the
 * permission, then the browser's subscription, then the controller. A
 * subscription the controller does not take is dropped from the browser.
 */
export async function enablePush(client: PushClient, browser: PushBrowser): Promise<EnableResult> {
	const key = await client.key();
	if (!key.ok) return { ok: false, why: 'controller', message: key.status === 404 ? 'This controller does not send push notifications.' : key.message };
	let perm: NotificationPermission;
	try {
		perm = await browser.requestPermission();
	} catch (e) {
		return { ok: false, why: 'browser', message: errText(e, 'the browser would not ask for permission') };
	}
	if (perm === 'denied') return { ok: false, why: 'denied', message: "Notifications are blocked for this site. Allow them in the browser's site settings." };
	if (perm !== 'granted') return { ok: false, why: 'dismissed', message: 'Permission was not given.' };
	let sub: PushSub;
	try {
		const reg = await browser.registration();
		// a subscription made under another key (a controller whose push key
		// changed) would be refused by every push: start from none
		const old = await reg.pushManager.getSubscription();
		if (old) await old.unsubscribe();
		sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(key.value) });
	} catch (e) {
		return { ok: false, why: 'browser', message: errText(e, 'the browser could not subscribe') };
	}
	const made = await client.subscribe(sub.toJSON());
	if (!made.ok) {
		try {
			await sub.unsubscribe();
		} catch {
			/* said below */
		}
		return { ok: false, why: 'controller', message: made.message };
	}
	return { ok: true, device: made.value };
}

/** Turn push off for this device: the controller first, then the browser's own. */
export async function disablePush(client: PushClient, browser: PushBrowser): Promise<{ ok: true } | { ok: false; message: string }> {
	let sub: PushSub | null = null;
	try {
		sub = await (await browser.registration()).pushManager.getSubscription();
	} catch {
		/* no worker: only the controller's row can be taken off by the card's list */
	}
	if (sub) {
		const r = await client.remove({ endpoint: sub.endpoint });
		// 404: the controller never had it (or already dropped it): the goal is met
		if (!r.ok && r.status !== 404) return { ok: false, message: r.message };
		try {
			await sub.unsubscribe();
		} catch (e) {
			return { ok: false, message: errText(e, 'the browser would not drop its subscription') };
		}
	}
	return { ok: true };
}

function errText(e: unknown, fallback: string): string {
	const m = (e as { message?: unknown } | null)?.message;
	return typeof m === 'string' && m ? m : fallback;
}

/**
 * The id the controller gives an endpoint (the first 16 octets of its
 * SHA-256, in hex), so the card can mark which listed phone is this one
 * without the controller ever returning an endpoint.
 */
export async function endpointId(endpoint: string, subtle: Pick<SubtleCrypto, 'digest'> = globalThis.crypto.subtle): Promise<string> {
	const d = new Uint8Array(await subtle.digest('SHA-256', new TextEncoder().encode(endpoint)));
	return Array.from(d.slice(0, 16), (b) => b.toString(16).padStart(2, '0')).join('');
}
