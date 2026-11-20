// Sign-in, elevation and the standby's referral, as the kit sees them
// (#708). No runes, no DOM: everything the browser does is injected, so the
// rules are tested on plain node in tests/auth.test.ts. `auth.svelte.ts` is
// the reactive wrapper an app mounts.
//
// The controller decides every request; nothing here grants anything. What
// the kit adds is the operator's side of the server's answers:
//
//   - a 401 that says `elevate: true` (an elevated permission needs the
//     password again) asks for the password, sends it to POST /api/elevate,
//     and repeats the refused request ONCE, byte for byte (a substitute's
//     requestId is the same one). Concurrent refusals share one prompt.
//     Cancel and the caller gets the original refusal;
//   - any other 401 is judged by a fresh GET /api/me, never by the 401
//     itself: a browser that had a session and now has none (signed out
//     elsewhere, expired, revoked, five wrong passwords) goes to the sign-in
//     page, which brings it back here;
//   - a 503 that says `standby: true` sends the page to the leader the
//     controller names (its configured public URL), never re-sending the
//     request there, and only to an http(s) URL that does not downgrade an
//     https page. A second follow within FOLLOW_GUARD_MS is not made (two
//     controllers each naming the other would bounce the page forever): the
//     referral is shown instead.
//
// A controller without an auth: block, or one too old to have /api/me,
// leaves everything exactly as it was.

import type { Fetch } from './kitfetch.js';

/** A browser sign-in, as /api/me reports it. */
export interface MeSession {
	/** When the sign-in ends (absolute), RFC 3339. */
	expires: string;
	/** When its password stops counting for the elevated permissions. */
	elevatedUntil: string;
}

/** GET /api/me. */
export interface Me {
	/** The controller has an auth: block. */
	auth: boolean;
	/** The request came over TLS (where a password may be sent). */
	secure: boolean;
	/** `anonymous`, `user:<name>`, `token`, `token:<name>`. */
	principal: string;
	roles: string[];
	/** Permission names held, by scope: `*` (everywhere) or an area name. */
	permissions: Record<string, string[]>;
	/** Present when the request acts by a browser sign-in. */
	session?: MeSession;
	/** The permissions a browser sign-in holds only while its password is fresh. */
	elevated?: string[];
	/** This controller offers browser sign-in (/signin). */
	signIn?: boolean;
	/** The controller's clock, RFC 3339. */
	now?: string;
}

/** A standby's referral the page did not (or could not) follow. */
export interface Referral {
	/** The leader's URL, when the controller named one that may be opened. */
	leader: string | null;
	/** What the controller said. */
	message: string;
}

/** The parsed JSON body of a refusal, or null. */
function refusalBody(body: string): Record<string, unknown> | null {
	try {
		const j = JSON.parse(body);
		return j && typeof j === 'object' && !Array.isArray(j) ? (j as Record<string, unknown>) : null;
	} catch {
		return null;
	}
}

/**
 * What a refusal says, for a person: the controller's JSON `error`, else its
 * text, else a line from the status (`failed (status)` for any but 401 and
 * 403). Never a guess at the cause.
 */
export function refusalMessage(status: number, body: string, failed = 'request failed'): string {
	const j = refusalBody(body.trim());
	const said = typeof j?.error === 'string' ? j.error.trim() : j ? '' : body.trim();
	if (said) return said;
	if (status === 401) return 'sign in, or send a token';
	if (status === 403) return 'not permitted';
	return `${failed} (${status})`;
}

/** A 401 asking for the password again (elevation). */
export function wantsElevation(status: number, body: string): boolean {
	return status === 401 && refusalBody(body)?.elevate === true;
}

/** A standby's 503 referral: the leader it names (maybe ""), or null when it is not one. */
export function standbyLeader(status: number, body: string): string | null {
	if (status !== 503) return null;
	const j = refusalBody(body);
	if (j?.standby !== true) return null;
	return typeof j.leader === 'string' ? j.leader : '';
}

/**
 * The URL to send the page to for a standby's referral, or null when the
 * leader it names may not be opened: anything but an absolute http(s) URL,
 * one carrying a user name or password, an http one from an https page, or
 * this page's own origin. The target is the leader's root, as the
 * controller's Location says; nothing of the current page goes with it.
 */
export function leaderTarget(leader: string | null | undefined, current: string): string | null {
	if (!leader) return null;
	let u: URL;
	let here: URL;
	try {
		u = new URL(leader); // no base: a relative or scheme-relative URL throws
		here = new URL(current);
	} catch {
		return null;
	}
	if (u.protocol !== 'http:' && u.protocol !== 'https:') return null;
	if (u.username || u.password) return null;
	if (here.protocol === 'https:' && u.protocol !== 'https:') return null;
	if (u.origin === here.origin) return null;
	return new URL('/', u.origin).href;
}

/** How long after following a referral another one is not followed. */
export const FOLLOW_GUARD_MS = 30_000;
/** Where the time of the last follow is kept (per origin, per tab). */
export const FOLLOW_KEY = 'nautilus.followedLeaderAt';

/** The subset of Storage the loop guard uses. */
export type GuardStorage = Pick<Storage, 'getItem' | 'setItem'>;

/** Whether a referral may be followed now (none followed from here within the guard). */
export function mayFollow(storage: GuardStorage | null | undefined, now: number): boolean {
	let last = NaN;
	try {
		const kept = storage?.getItem(FOLLOW_KEY);
		if (kept) last = Number(kept); // none kept is no guard (Number(null) is 0)
	} catch {
		/* storage blocked: no guard to read; follow */
	}
	return !(Number.isFinite(last) && now >= last && now - last < FOLLOW_GUARD_MS);
}

/** The sign-in page, coming back to here afterwards (the server checks `next`). */
export function signInHref(here: { pathname: string; search: string }): string {
	return '/signin?next=' + encodeURIComponent(here.pathname + here.search);
}

/** Whether `me` holds `perm` anywhere (everywhere, or in some area). */
export function holdsAnywhere(me: Me | null | undefined, perm: string): boolean {
	if (!me) return false;
	return Object.values(me.permissions ?? {}).some((ps) => ps.includes(perm));
}

/** The controller's clock minus the browser's, in ms (0 when it does not say). */
export function clockSkew(me: Me | null | undefined, localNow: number): number {
	const t = me?.now ? Date.parse(me.now) : NaN;
	return Number.isFinite(t) ? t - localNow : 0;
}

/**
 * Whether an action needing `perm` will ask for the password first: a
 * browser sign-in that holds `perm`, which is elevated, past its
 * elevatedUntil by the CONTROLLER's clock (`skew` from clockSkew). A token
 * never does; nor does a permission that is not elevated.
 */
export function needsPassword(me: Me | null | undefined, perm: string, localNow: number, skew = 0): boolean {
	if (!me?.session || !me.elevated?.includes(perm) || !holdsAnywhere(me, perm)) return false;
	const until = Date.parse(me.session.elevatedUntil);
	return !Number.isFinite(until) || until <= localNow + skew;
}

/** What the password prompt is told. */
export interface PasswordAsk {
	/** The signed-in user, from the principal. */
	user: string;
	/** Why the last attempt failed, for a second ask. */
	error?: string;
}

export interface AuthDeps {
	/** The browser's fetch (never the kit's: this is what it wraps). */
	fetch: Fetch;
	/** Ask for the password; null when the operator cancels (or nobody can ask). */
	prompt: (ask: PasswordAsk) => Promise<string | null>;
	/** Send the page somewhere (location.assign). */
	navigate: (href: string) => void;
	/** The page's location. */
	location: () => { href: string; pathname: string; search: string };
	/** sessionStorage, for the referral loop guard. */
	storage?: GuardStorage | null;
	now?: () => number;
	/** Called whenever me, referral or supported changes. */
	onchange?: () => void;
}

/** The first probe's wait after a stream error, and the longest. */
export const PROBE_MIN_MS = 2_000;
export const PROBE_MAX_MS = 60_000;

/** The sign-in rules, over injected browser parts. See the file comment. */
export class AuthSession {
	/** The last /api/me, or null before it (or on a controller without one). */
	me: Me | null = null;
	/** False once /api/me answered 404: a controller without it; the kit does nothing. */
	supported = true;
	/** A referral that was not followed, for a banner; null otherwise. */
	referral: Referral | null = null;

	readonly #d: AuthDeps;
	#hadSession = false;
	#elevating: Promise<boolean> | null = null;
	#leaving = false;
	#probeWait = PROBE_MIN_MS;
	#nextProbe = 0;

	constructor(deps: AuthDeps) {
		this.#d = deps;
	}

	#now(): number {
		return this.#d.now?.() ?? Date.now();
	}

	#changed() {
		this.#d.onchange?.();
	}

	/** Whether a URL is this page's origin (only those are prompted for or followed). */
	#sameOrigin(input: RequestInfo | URL): boolean {
		const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
		try {
			const here = new URL(this.#d.location().href);
			return new URL(raw, here).origin === here.origin;
		} catch {
			return false;
		}
	}

	/** GET /api/me, afresh. Null when there is no answer to go by. */
	async refresh(): Promise<Me | null> {
		let res: Response;
		try {
			res = await this.#d.fetch('/api/me', { headers: { Accept: 'application/json' }, cache: 'no-store' });
		} catch {
			return this.me; // unreachable: nothing new to go by
		}
		if (res.status === 404) {
			this.supported = false;
			this.me = null;
			this.#changed();
			return null;
		}
		const text = await res.text();
		if (!res.ok) {
			const leader = standbyLeader(res.status, text);
			if (leader !== null) this.#refer(leader, refusalMessage(res.status, text));
			return null;
		}
		try {
			this.me = JSON.parse(text) as Me;
		} catch {
			return this.me;
		}
		this.supported = true;
		if (this.me.session) this.#hadSession = true;
		this.#changed();
		return this.me;
	}

	/**
	 * Decide from a fresh /api/me whether this browser was signed out, and if
	 * so send it to the sign-in page. Reports whether it did.
	 */
	async checkSignedOut(): Promise<boolean> {
		const had = this.#hadSession;
		const me = await this.refresh();
		if (!me || !me.auth || me.session || !had || !me.signIn) return false;
		this.#hadSession = false;
		this.#leave(signInHref(this.#d.location()));
		return true;
	}

	#leave(href: string) {
		if (this.#leaving) return;
		this.#leaving = true;
		this.#d.navigate(href);
	}

	/** A standby referred us: follow it, once, or keep it for a banner. */
	#refer(leader: string, message: string) {
		const target = leaderTarget(leader, this.#d.location().href);
		const now = this.#now();
		if (target && mayFollow(this.#d.storage, now)) {
			try {
				this.#d.storage?.setItem(FOLLOW_KEY, String(now));
			} catch {
				/* no guard can be kept; follow anyway */
			}
			this.#leave(target);
			return;
		}
		this.referral = { leader: target, message };
		this.#changed();
	}

	/**
	 * Ask for the password and prove it (POST /api/elevate). Concurrent
	 * callers share one prompt. Resolves true once proved.
	 */
	elevate(): Promise<boolean> {
		this.#elevating ??= this.#runElevate().finally(() => {
			this.#elevating = null;
		});
		return this.#elevating;
	}

	async #runElevate(): Promise<boolean> {
		const user = (this.me?.principal ?? '').replace(/^user:/, '');
		let error: string | undefined;
		for (;;) {
			const password = await this.#d.prompt({ user, error });
			if (password === null) return false;
			let res: Response;
			try {
				res = await this.#d.fetch('/api/elevate', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ password })
				});
			} catch {
				error = 'controller unreachable';
				continue;
			}
			const text = await res.text();
			if (res.ok) {
				await this.refresh();
				return true;
			}
			const msg = refusalMessage(res.status, text);
			if (res.status === 401) {
				// A wrong password, or the sign-in has ended (five wrong in a
				// row, revoked, expired): the controller says which by /api/me.
				if (await this.checkSignedOut()) return false;
				error = msg;
				continue;
			}
			if (res.status === 429 || res.status === 503) {
				error = msg; // throttled or busy: the operator may try again
				continue;
			}
			// 400, 403, 404: a password cannot help here (cross-origin, not
			// over TLS, nothing to confirm). The caller keeps its refusal.
			return false;
		}
	}

	/**
	 * fetch with the rules in front. Hand it to a client, or let
	 * `createAuth()` install it as the kit's fetch.
	 */
	fetch: Fetch = async (input, init) => {
		// A Request's body can be read once: keep a copy for the retry.
		const again = input instanceof Request ? input.clone() : input;
		const res = await this.#d.fetch(input, init);
		if (res.status !== 401 && res.status !== 503) return res;
		if (!this.supported || !this.#sameOrigin(input)) return res;
		const text = await res.clone().text();
		if (res.status === 503) {
			const leader = standbyLeader(res.status, text);
			if (leader !== null) this.#refer(leader, refusalMessage(res.status, text));
			return res;
		}
		if (wantsElevation(res.status, text)) {
			if (!(await this.elevate())) return res;
			return this.#d.fetch(again, init); // once; its answer is the answer
		}
		void this.checkSignedOut();
		return res;
	};

	/**
	 * A live stream errored. Its status cannot be read, so ask /api/me, at
	 * most once per wait; the wait doubles to PROBE_MAX_MS while the stream
	 * stays down.
	 */
	streamTrouble(): void {
		const now = this.#now();
		if (!this.supported || now < this.#nextProbe) return;
		this.#nextProbe = now + this.#probeWait;
		this.#probeWait = Math.min(this.#probeWait * 2, PROBE_MAX_MS);
		void this.checkSignedOut();
	}

	/** The stream is open again: the next error probes at once. */
	streamFine(): void {
		this.#probeWait = PROBE_MIN_MS;
		this.#nextProbe = 0;
	}
}
