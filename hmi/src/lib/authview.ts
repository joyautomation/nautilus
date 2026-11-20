// What a control asks before it renders (#708 PR 7b): may the signed-in
// caller do this, and will it ask for the password first. Plain TS, so the
// decision is tested on node (tests/areas.test.ts); `auth.svelte.ts`
// installs the view at load, reading its runes, so a component that calls
// these inside \$derived re-renders when the sign-in changes.
//
// A convenience only: the controller decides every request.

import { can, deniedReason } from './areas.js';
import { needsPassword, type Me } from './auth.js';

/** What the decisions read: the started AuthClient's state, or null. */
export interface AuthView {
	readonly me: Me | null;
	readonly supported: boolean;
	/** The browser's clock, ms (ticks while started). */
	readonly clock: number;
	/** The controller's clock minus the browser's, ms. */
	readonly skew: number;
}

let view: () => AuthView | null = () => null;

/** Install where the view comes from (auth.svelte.ts does, once). */
export function setAuthView(get: () => AuthView | null): void {
	view = get;
}

/**
 * Why the caller may not do `perm` (on `target`), or '' when they may, when
 * no `createAuth()` is started, or on a controller without /api/me.
 */
export function deniedFor(perm: string, target?: string): string {
	const v = view();
	if (!v || !v.supported) return '';
	return can(v.me, perm, target) ? '' : deniedReason(v.me, perm, target);
}

/** Whether an action needing `perm` will ask for the password first, by the controller's clock. */
export function asksPassword(perm: string): boolean {
	const v = view();
	if (!v || !v.supported) return false;
	return needsPassword(v.me, perm, v.clock, v.skew);
}
