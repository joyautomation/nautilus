// The user-components build harness: compiles a set of `{Name}.svelte`
// files INTO a single self-contained IIFE using the USER'S OWN toolchain —
// their project's `svelte/compiler` and node_modules, resolved per file with
// `createRequire`, so bare imports (svelte, @joyautomation/nautilus-hmi,
// anything else) come from wherever the component actually lives. No vscode
// import here — this module is pure Node + esbuild + the resolved svelte
// compiler, so it's directly unit-testable end to end against a real,
// unmodified `.svelte` fixture (see userComponentBuild.test.ts, which
// compiles testdata/user-components/Supply.svelte through this exact
// function).
//
// Failure handling is per-component: a component that fails to resolve its
// project's svelte/compiler, or fails to compile, is EXCLUDED from the
// generated entry (so it's simply absent from window.__NX_USER_COMPONENTS__
// — the webview falls back to its placeholder chip) rather than aborting
// the whole build. Anything unexpected during the esbuild pass itself
// (a bad bare import, a nested .svelte dependency that fails) is caught
// per-file inside the plugin and swapped for an empty stub — the bundle
// always finishes and always gets written, per "never crash the editor".

import { createRequire } from "module";
import * as fs from "fs";
import * as path from "path";
import * as esbuild from "esbuild";

export type ComponentDiagnostic = { component: string; message: string };
export type BuildResult = { diagnostics: ComponentDiagnostic[]; built: string[] };

/** The bare shape of `svelte/compiler` we call — kept minimal and untyped
 * against the real package so this file has no compile-time dependency on
 * whichever svelte version a user's project happens to pin. */
type SvelteCompilerModule = {
  compile: (source: string, options: Record<string, unknown>) => { js: { code: string } };
  compileModule: (source: string, options: Record<string, unknown>) => { js: { code: string } };
};

/** Resolve `svelte/compiler` starting the node_modules walk-up from
 * `fromFile`'s directory — the USER's own installed svelte, never ours. */
function resolveSvelteCompiler(fromFile: string): SvelteCompilerModule {
  const req = createRequire(fromFile);
  return req("svelte/compiler") as SvelteCompilerModule;
}

type CompiledFile = { code: string; resolveDir: string };

/** Compile one `.svelte` / `.svelte.js` / `.svelte.ts` file with the
 * project's own compiler, caching by absolute path (the entry's own
 * dependency graph can reference the same file more than once). Throws on
 * any failure — callers decide what "still work anyway" means.
 *
 * TypeScript: for a `.svelte` component, `<script lang="ts">` (including
 * TS-only syntax in markup expressions, e.g. `as HTMLInputElement` casts)
 * is handled by the svelte compiler's OWN TypeScript-aware parser — no
 * preprocessing needed (verified against this repo's kit components, which
 * lean on exactly that). `compileModule` (for `.svelte.js`/`.svelte.ts`
 * plain runes modules) has no such built-in TS support, so those get
 * transpiled with esbuild first — the one place this harness's own esbuild
 * (not the user's toolchain) does real work. */
async function compileSvelteFile(absPath: string, cache: Map<string, CompiledFile>): Promise<CompiledFile> {
  const cached = cache.get(absPath);
  if (cached) return cached;
  const compiler = resolveSvelteCompiler(absPath);
  const source = await fs.promises.readFile(absPath, "utf8");
  const resolveDir = path.dirname(absPath);

  let result: CompiledFile;
  if (/\.svelte\.(js|ts)$/.test(absPath)) {
    let src = source;
    if (absPath.endsWith(".ts")) {
      src = (
        await esbuild.transform(source, { loader: "ts", tsconfigRaw: { compilerOptions: { target: "esnext" } } })
      ).code;
    }
    const out = compiler.compileModule(src, { filename: absPath, generate: "client" });
    result = { code: out.js.code, resolveDir };
  } else {
    const out = compiler.compile(source, { filename: absPath, generate: "client", css: "injected" });
    result = { code: out.js.code, resolveDir };
  }
  cache.set(absPath, result);
  return result;
}

function describeError(err: unknown): string {
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
function buildEntrySource(usable: [name: string, absPath: string][]): string {
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

const EMPTY_BUNDLE_JS =
  "window.__NX_USER_COMPONENTS__ = window.__NX_USER_COMPONENTS__ || {};\n" +
  "window.dispatchEvent(new Event('nx:user-components-ready'));\n";

/** Compile `components` (name -> absolute .svelte path) into one IIFE at
 * `outfile`, using each component's OWN project toolchain. Always writes
 * `outfile` — with whichever components compiled successfully, or the empty
 * stub if none did — and never throws; every failure becomes a diagnostic
 * instead. */
export async function buildUserComponentsBundle(
  components: Record<string, string>,
  outfile: string
): Promise<BuildResult> {
  const diagnostics: ComponentDiagnostic[] = [];
  const cache = new Map<string, CompiledFile>();
  const usable: [string, string][] = [];

  for (const [name, absPath] of Object.entries(components)) {
    try {
      await compileSvelteFile(absPath, cache);
      usable.push([name, absPath]);
    } catch (err) {
      diagnostics.push({ component: name, message: describeError(err) });
    }
  }

  await fs.promises.mkdir(path.dirname(outfile), { recursive: true });

  if (usable.length === 0) {
    await fs.promises.writeFile(outfile, EMPTY_BUNDLE_JS, "utf8");
    return { diagnostics, built: [] };
  }

  const entryDir = path.dirname(usable[0][1]);
  const entryCode = buildEntrySource(usable);
  const ENTRY_SPECIFIER = "nx-user-components-entry";

  const plugin: esbuild.Plugin = {
    name: "nx-user-components",
    setup(build) {
      build.onResolve({ filter: new RegExp(`^${ENTRY_SPECIFIER}$`) }, () => ({
        path: ENTRY_SPECIFIER,
        namespace: "nx-entry",
      }));
      build.onLoad({ filter: /.*/, namespace: "nx-entry" }, async () => {
        try {
          const compiler = resolveSvelteCompiler(usable[0][1]);
          const out = compiler.compileModule(entryCode, {
            filename: "nx-user-components-entry.js",
            generate: "client",
          });
          return { contents: out.js.code, loader: "js", resolveDir: entryDir };
        } catch (err) {
          diagnostics.push({ component: "*", message: "compiling the user-components entry: " + describeError(err) });
          return { contents: "", loader: "js", resolveDir: entryDir };
        }
      });
      build.onLoad({ filter: /\.svelte(\.(js|ts))?$/ }, async (args) => {
        try {
          const compiled = await compileSvelteFile(args.path, cache);
          return { contents: compiled.code, loader: "js", resolveDir: compiled.resolveDir };
        } catch (err) {
          const base = path.basename(args.path).replace(/\.svelte(\.(js|ts))?$/, "");
          diagnostics.push({ component: base, message: describeError(err) });
          return { contents: "export default {};", loader: "js", resolveDir: path.dirname(args.path) };
        }
      });
    },
  };

  try {
    await esbuild.build({
      entryPoints: [ENTRY_SPECIFIER],
      bundle: true,
      format: "iife",
      platform: "browser",
      outfile,
      write: true,
      logLevel: "silent",
      absWorkingDir: entryDir,
      plugins: [plugin],
    });
  } catch (err) {
    diagnostics.push({ component: "*", message: "bundling user components: " + describeError(err) });
    await fs.promises.writeFile(outfile, EMPTY_BUNDLE_JS, "utf8");
    return { diagnostics, built: [] };
  }

  return { diagnostics, built: usable.map(([name]) => name) };
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
