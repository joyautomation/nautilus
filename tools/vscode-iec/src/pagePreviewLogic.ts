// The pure part of "Preview HMI Page" (pagePreview.ts): which SvelteKit
// project a file belongs to, which route a `src/routes/**` file renders,
// and the dev-server command line. No vscode import — unit-tested in
// pagePreviewLogic.test.ts.
//
// A PAGE is not previewed the way a component is (componentPreview.ts
// compiles one .svelte in isolation): a page needs its layout chain, the
// kit's `$lib`/`$app` aliases, the theme stylesheets, and the realtime
// client talking to a controller over `/api/stream`. All of that is what
// the project's own `vite dev` already does, with HMR — so the page is
// previewed FOR USE: the real dev server, the real route, in VS Code's
// Simple Browser beside the text editor.

import * as path from "path";

const CONFIG_NAMES = ["svelte.config.js", "svelte.config.ts", "svelte.config.mjs", "svelte.config.cjs"];

/** The nearest directory at or above `fsPath` that holds a svelte.config.*
 * — the SvelteKit project root — or null when none does. `exists` is
 * injected so the walk is testable without a filesystem. */
export function svelteKitRootFor(fsPath: string, exists: (p: string) => boolean): string | null {
  let dir = path.dirname(fsPath);
  for (;;) {
    if (CONFIG_NAMES.some((n) => exists(path.join(dir, n)))) return dir;
    const parent = path.dirname(dir);
    if (parent === dir) return null;
    dir = parent;
  }
}

/** The route a file under `<root>/src/routes` renders: its directory
 * relative to `routes`, with SvelteKit's `(group)` segments dropped. A
 * `[param]` segment stays as written — there is no value to fill it with
 * — so the caller can say so. `/` for the root page; null when the file is
 * not under `src/routes` at all. */
export function routeForFile(fsPath: string, root: string): string | null {
  const routes = path.join(root, "src", "routes");
  const rel = path.relative(routes, path.dirname(fsPath));
  if (rel.startsWith("..") || path.isAbsolute(rel)) return null;
  const segs = rel
    .split(path.sep)
    .filter((s) => s !== "" && s !== ".")
    .filter((s) => !(s.startsWith("(") && s.endsWith(")")));
  return "/" + segs.join("/");
}

/** Whether a route still carries a `[param]` the preview can't fill. */
export function routeHasParams(route: string): boolean {
  return /\[[^\]]*\]/.test(route);
}

/** The `npm run dev` invocation for one preview server: a port we chose
 * (so nothing has to parse Vite's banner) and strictPort so a clash fails
 * loudly instead of silently moving. */
export function devServerArgs(port: number): string[] {
  return ["run", "dev", "--", "--port", String(port), "--strictPort"];
}

/** The URL to open for `route` on the dev server at `port`. `localhost`,
 * not 127.0.0.1: Vite's default host binds both loopbacks, and
 * `vscode.env.asExternalUri` forwards localhost ports from a remote. */
export function pageUrl(port: number, route: string): string {
  return `http://localhost:${port}${route}`;
}

/** The nearest directory at or above `dir` that holds a nautilus.yaml —
 * where `naut run .` belongs — or null. */
export function nautilusProjectAbove(dir: string, exists: (p: string) => boolean): string | null {
  let d = dir;
  for (;;) {
    if (exists(path.join(d, "nautilus.yaml"))) return d;
    const parent = path.dirname(d);
    if (parent === d) return null;
    d = parent;
  }
}
