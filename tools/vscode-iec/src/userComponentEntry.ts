// The user-components bundle's parts that need no Node: what a build
// reports, and the entry that registers the components on
// window.__NX_USER_COMPONENTS__ for the mimic editor's islands. Shared by
// the desktop build (userComponentBuild.ts: the project's own svelte and
// esbuild) and the browser's (web/componentCompiler.ts: svelte/compiler
// and Sucrase in the extension host), so both hand the editor the same
// thing.

export type ComponentDiagnostic = { component: string; message: string };
export type BuildResult = { diagnostics: ComponentDiagnostic[]; built: string[] };

export function describeError(err: unknown): string {
  if (err && typeof err === "object" && "message" in err && typeof (err as { message: unknown }).message === "string") {
    return (err as { message: string }).message;
  }
  return String(err);
}

/** The virtual entry — compiled itself as a runes module (compileModule),
 * since `$state` needs the svelte compiler's transform, not plain esbuild.
 * Exposes `window.__NX_USER_COMPONENTS__[name] = { mount(target, props) }`
 * where the handle wraps a `$state` props object: mutating it after mount
 * (handle.update) flows reactively into the mounted instance — the
 * documented Svelte 5 pattern for programmatic prop updates. */
export function buildEntrySource(usable: [name: string, absPath: string][]): string {
  const imports = usable.map(([, absPath], i) => `import C${i} from ${JSON.stringify(absPath)};`).join("\n");
  const entries = usable.map(([name], i) => `${JSON.stringify(name)}: C${i}`).join(", ");
  return `import { mount, unmount } from 'svelte';
${imports}

const registry = { ${entries} };

function makeHandle(Component, target, initial) {
	let props = $state({ ...(initial ?? {}) });
	const instance = mount(Component, { target, props });
	return {
		update(partial) {
			Object.assign(props, partial ?? {});
		},
		destroy() {
			unmount(instance);
		}
	};
}

const api = {};
for (const name of Object.keys(registry)) {
	const Component = registry[name];
	api[name] = { mount: (target, props) => makeHandle(Component, target, props) };
}
window.__NX_USER_COMPONENTS__ = api;
window.dispatchEvent(new Event('nx:user-components-ready'));
`;
}

export const EMPTY_BUNDLE_JS =
  "window.__NX_USER_COMPONENTS__ = window.__NX_USER_COMPONENTS__ || {};\n" +
  "window.dispatchEvent(new Event('nx:user-components-ready'));\n";

/** A compiler warning that means the component throws when it mounts: the
 * build stops on it with the warning's own words and where it is, rather
 * than handing the editor a component that blanks every one placed after
 * it. `store_rune_conflict` is the one met so far: a prop or variable named
 * `state` (a filter's state, say) beside `$state(...)` makes Svelte read
 * `$state` as a store subscription to it, which throws at mount
 * ("e.subscribe is not a function"). */
const FATAL_WARNINGS = new Set(["store_rune_conflict"]);

export type CompilerWarning = { code?: string; message?: string; start?: { line: number; column: number } };

export function fatalWarning(path: string, warnings: readonly CompilerWarning[] | undefined): Error | undefined {
  const w = warnings?.find((x) => x.code && FATAL_WARNINGS.has(x.code));
  if (!w) return undefined;
  const at = w.start ? `:${w.start.line}:${w.start.column + 1}` : "";
  const said = (w.message ?? w.code ?? "").replace(/\s*\n\s*/g, " ");
  return new Error(`${path}${at}: ${said} (${w.code}: it throws when it mounts)`);
}

/** Discovery's pure part: the referenced names that need a build, i.e. not
 * already covered by the built-in kit registry. */
export function customComponentNames(referenced: Iterable<string>, builtins: ReadonlySet<string>): string[] {
  const out = new Set<string>();
  for (const n of referenced) {
    if (n && !builtins.has(n)) out.add(n);
  }
  return [...out].sort();
}
