// Which build this is. The web build (esbuild.web.mjs) swaps this module for
// web/platform.ts, the way GOOS picks a file in the Go tree: code that only
// makes sense with a real `naut` and a real machine (installing the CLI,
// terminals, git, Logix, compiling user components) asks IS_WEB and stays
// out of the browser.
export const IS_WEB = false;

/** The platform's own commands and UI; nothing on a real machine. */
export function registerPlatform(_context: unknown, _live: unknown): void {}
