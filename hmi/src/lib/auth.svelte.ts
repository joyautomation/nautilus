// `createAuth()`: the kit's side of sign-in (#708), mounted once in the root
// layout, next to `<ConfirmDialog/>`:
//
//     const auth = createAuth();
//     onMount(() => auth.start()); // and auth.stop() on destroy
//
// It installs its fetch as the kit's (every kit client then asks for the
// password when an elevated action needs it, and follows a sign-out or a
// standby's referral), reports a live stream's errors to it, and keeps
// `me` (GET /api/me) as a rune. The rules themselves are in ./auth.ts.
//
// Nothing here grants anything: the controller decides every request. A
// control the kit greys out for a role is a convenience; the server's
// refusal is the answer.
import { askPassword } from './confirm.svelte.js';
import { setKitFetch, setStreamHooks, type Fetch } from './kitfetch.js';
import { AuthSession, clockSkew, holdsAnywhere, needsPassword, type AuthDeps, type Me, type Referral } from './auth.js';
import { can, deniedReason } from './areas.js';

let current = $state<AuthClient | null>(null);

/**
 * The started `AuthClient`, or null: what the kit's controls ask (no prop to
 * thread through every faceplate). Reactive.
 */
export function currentAuth(): AuthClient | null {
	return current;
}

/**
 * Why the signed-in caller may not do `perm` (on `target`), or '' when they
 * may, or when no `createAuth()` is started. What a control greys itself out
 * with. Reactive; a convenience: the controller decides.
 */
export function deniedFor(perm: string, target?: string): string {
	const a = current;
	if (!a || a.can(perm, target)) return '';
	return a.deniedReason(perm, target);
}

/** Whether an action needing `perm` will ask for the password first (false with no `createAuth()`). Reactive. */
export function asksPassword(perm: string): boolean {
	return current?.needsPassword(perm) ?? false;
}

export interface AuthOptions {
	/** Ask for the password some other way. Default: the `<ConfirmDialog/>`. */
	prompt?: AuthDeps['prompt'];
	/** The fetch it wraps. Default: the browser's. */
	fetch?: Fetch;
}

/** The reactive half: `me`, `referral`, `supported` are runes. */
export class AuthClient {
	/** The last /api/me, or null. */
	me = $state<Me | null>(null);
	/** False on a controller without /api/me: the kit then does nothing. */
	supported = $state(true);
	/** A standby's referral that was not followed (for a banner), or null. */
	referral = $state<Referral | null>(null);
	/** The controller's clock minus the browser's, ms. */
	skew = $state(0);
	/** The browser's clock, ticking while started, so a password badge appears when the window lapses. */
	clock = $state(Date.now());
	#tick: ReturnType<typeof setInterval> | null = null;

	readonly session: AuthSession;

	constructor(opts: AuthOptions = {}) {
		const raw: Fetch = opts.fetch ?? ((input, init) => globalThis.fetch(input, init));
		this.session = new AuthSession({
			fetch: raw,
			prompt:
				opts.prompt ??
				((ask) =>
					askPassword({
						title: 'Confirm your password',
						body: `This action needs your password again${ask.user ? ` (${ask.user})` : ''}.`,
						confirmLabel: 'Confirm',
						error: ask.error
					})),
			navigate: (href) => globalThis.location.assign(href),
			location: () => globalThis.location,
			storage: (() => {
				try {
					return globalThis.sessionStorage;
				} catch {
					return null;
				}
			})(),
			onchange: () => {
				this.me = this.session.me;
				this.supported = this.session.supported;
				this.referral = this.session.referral;
				this.skew = clockSkew(this.session.me, Date.now());
			}
		});
	}

	/** fetch with the sign-in rules in front (what `start` installs for the kit). */
	get fetch(): Fetch {
		return this.session.fetch;
	}

	/** Install it as the kit's fetch and stream listener, and read /api/me. */
	start(): Promise<Me | null> {
		current = this;
		this.#tick ??= setInterval(() => (this.clock = Date.now()), 15_000);
		setKitFetch(this.session.fetch);
		setStreamHooks(
			() => this.session.streamTrouble(),
			() => this.session.streamFine()
		);
		return this.session.refresh();
	}

	/** Uninstall: the kit's fetch is the browser's again. */
	stop(): void {
		if (current === this) current = null;
		if (this.#tick) clearInterval(this.#tick);
		this.#tick = null;
		setKitFetch(null);
		setStreamHooks(null);
	}

	/** Read /api/me again (after a sign-in elsewhere, say). */
	refresh(): Promise<Me | null> {
		return this.session.refresh();
	}

	/** Whether the caller holds `perm` anywhere. Without auth: (or no /api/me), true. */
	holds(perm: string): boolean {
		if (!this.supported || !this.me || !this.me.auth) return true;
		return holdsAnywhere(this.me, perm);
	}

	/** Whether an action needing `perm` will ask for the password first (by the controller's clock). */
	needsPassword(perm: string): boolean {
		return needsPassword(this.me, perm, this.clock, this.skew);
	}

	/**
	 * Whether the caller may do `perm` on `target` (a tag, member path or
	 * element), or on the whole controller without one. True without auth:
	 * (or /api/me). A convenience: the controller decides.
	 */
	can(perm: string, target?: string): boolean {
		if (!this.supported) return true;
		return can(this.me, perm, target);
	}

	/** Why `can` said no, in words. */
	deniedReason(perm: string, target?: string): string {
		return deniedReason(this.me, perm, target);
	}

	/** The signed-in user's name, or null (a token, anonymous, no auth:). */
	get user(): string | null {
		const p = this.me?.principal ?? '';
		return p.startsWith('user:') ? p.slice(5) : null;
	}

	/** Sign out, then the sign-in page. Resolves null, or the reason it could not. */
	signOut(): Promise<string | null> {
		return this.session.signOut();
	}
}

/** Convenience factory mirroring `new AuthClient(opts)`. */
export function createAuth(opts: AuthOptions = {}): AuthClient {
	return new AuthClient(opts);
}
