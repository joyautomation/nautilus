// Reactive bridge to window.__NX_USER_COMPONENTS__ — the IIFE the extension
// host builds from the user's OWN Svelte components (src/userComponents.ts +
// src/userComponentBuild.ts) and injects as a second <script> tag AFTER the
// editor bundle's. Because that tag necessarily loads (and runs) later, the
// registry isn't there yet at first paint: this module's `tick` bumps on the
// entry's "ready" event (dispatched once window.__NX_USER_COMPONENTS__ is
// set) so every UserIsland re-checks availability instead of being stuck on
// the fallback chip forever. Compile diagnostics arrive as an ordinary
// postMessage (userComponentDiagnostics) and surface as the chip's tooltip.
export type UserComponentHandle = { update(props: Record<string, unknown>): void; destroy(): void };
type UserComponentApi = { mount(target: Element, props: Record<string, unknown>): UserComponentHandle };

declare global {
	interface Window {
		__NX_USER_COMPONENTS__?: Record<string, UserComponentApi>;
	}
}

export const ucState = $state({
	tick: 0,
	diagnostics: {} as Record<string, string>,
	// The host has a build due or running (userComponents.ts isBuilding()):
	// a component not in the bundle yet is on its way, not missing.
	building: false,
	// This page was served with a bundle script, which may not have run yet
	// (the host marks <body data-user-bundle>: a fact about this page, not
	// about the next one a rebuild is about to load).
	bundle: typeof document !== 'undefined' && !!document.body?.hasAttribute('data-user-bundle'),
	// Every script on the page has run or failed (window "load"): a bundle
	// that has not registered by then never will (it threw, or 404'd).
	loaded: typeof document !== 'undefined' && document.readyState === 'complete'
});

if (typeof window !== 'undefined' && !ucState.loaded) {
	window.addEventListener('load', () => (ucState.loaded = true), { once: true });
}

if (typeof window !== 'undefined') {
	window.addEventListener('nx:user-components-ready', () => {
		ucState.tick++;
	});
}

/** Reactive: true once `name` has a real user-authored island to mount
 * (reads `ucState.tick` so callers inside `$derived`/`$effect` re-run when
 * a (re)built bundle lands). */
export function hasUserComponent(name: string): boolean {
	ucState.tick;
	return typeof window !== 'undefined' && !!window.__NX_USER_COMPONENTS__?.[name];
}

export function userComponentApi(name: string): UserComponentApi | undefined {
	return window.__NX_USER_COMPONENTS__?.[name];
}

/** The host's last reported compile failure for `name`, if any — shown as
 * the fallback chip's tooltip. */
export function userComponentDiagnostic(name: string): string | undefined {
	return ucState.diagnostics[name];
}

/** Apply a `userComponentDiagnostics` message's payload. */
export function setUserComponentDiagnostics(diagnostics: { component: string; message: string }[]): void {
	ucState.diagnostics = Object.fromEntries(diagnostics.map((d) => [d.component, d.message]));
}

/** Apply the host's build status (sent with the diagnostics). */
export function setUserComponentStatus(status: { building?: boolean }): void {
	if (status.building !== undefined) ucState.building = status.building;
}

/** Reactive: `name` has no island yet but one is coming: a build is due or
 * running, or the built script has not run yet (once the page has loaded,
 * a bundle that never registered is a failure). Shown as a loading chip,
 * not the red "unknown component" one. */
export function userComponentPending(name: string): boolean {
	ucState.tick;
	if (hasUserComponent(name) || ucState.diagnostics[name]) return false;
	return ucState.building || (ucState.bundle && !ucState.loaded && typeof window !== 'undefined' && !window.__NX_USER_COMPONENTS__);
}
