// What a component compiled in the browser can import without it being in
// the project: Svelte and the nautilus HMI kit, built once into
// media/dist/user-runtime.js (webview-ui/vite.runtime.config.ts, which
// reads this list) and published there as window.__NX_RT__[specifier].
// web/componentCompiler.ts resolves these to that object and anything
// else bare to a diagnostic. runtimeModules.test.ts keeps the list in step
// with the svelte and kit packages' own exports.

/** Svelte's client-side entry points: its package exports, less the
 * compiler, the server renderer, type-only entries, and svelte/internal
 * (Svelte 4's private API: importing it throws). */
export const SVELTE_MODULES = [
  "svelte",
  "svelte/animate",
  "svelte/attachments",
  "svelte/easing",
  "svelte/events",
  "svelte/internal/client",
  "svelte/internal/disclose-version",
  "svelte/internal/flags/async",
  "svelte/internal/flags/legacy",
  "svelte/internal/flags/tracing",
  "svelte/legacy",
  "svelte/motion",
  "svelte/reactivity",
  "svelte/reactivity/window",
  "svelte/store",
  "svelte/transition",
] as const;

/** The kit's JavaScript entry points. */
export const KIT_MODULES = ["@joyautomation/nautilus-hmi", "@joyautomation/nautilus-hmi/mimic"] as const;

/** The kit's stylesheets: the mimic editor already loads its styles, so an
 * import of one is a no-op here. */
export const KIT_STYLES = [
  "@joyautomation/nautilus-hmi/theme.css",
  "@joyautomation/nautilus-hmi/palette.css",
  "@joyautomation/nautilus-hmi/fonts.css",
] as const;

export const RUNTIME_MODULES: readonly string[] = [...SVELTE_MODULES, ...KIT_MODULES];
