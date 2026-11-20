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
		setKitFetch(this.session.fetch);
		setStreamHooks(
			() => this.session.streamTrouble(),
			() => this.session.streamFine()
		);
		return this.session.refresh();
	}

	/** Uninstall: the kit's fetch is the browser's again. */
	stop(): void {
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
		return needsPassword(this.me, perm, Date.now(), this.skew);
	}

	/** Sign out (POST /api/signout), then go to the sign-in page. */
	async signOut(): Promise<void> {
		try {
			await this.session.fetch('/api/signout', { method: 'POST' });
		} finally {
			globalThis.location.assign('/signin');
		}
	}
}

/** Convenience factory mirroring `new AuthClient(opts)`. */
export function createAuth(opts: AuthOptions = {}): AuthClient {
	return new AuthClient(opts);
}
