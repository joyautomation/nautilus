// The browser's user-components build: what userComponentBuild.ts does on a
// desktop, without Node. Svelte's own compiler turns each .svelte file into
// JavaScript, Sucrase strips TypeScript and turns ES modules into CommonJS,
// and a small registry links them into one script that registers the
// components on window.__NX_USER_COMPONENTS__, exactly as the desktop
// bundle does (the entry is the same: userComponentEntry.ts).
//
// Svelte and the HMI kit are not bundled: they come from
// window.__NX_RT__, built once into media/dist/user-runtime.js
// (runtimeModules.ts). A project-relative import (./x, ../x, $lib/x)
// resolves through the project; any other bare import is a diagnostic for
// the component that made it, which is left a placeholder.
//
// The playground compiles with its own Svelte, not the version the
// project's package.json asks for.
//
// No vscode import: web/userComponents.ts loads this file only when a mimic
// needs a custom component (it is a chunk of its own, out/web/
// component-compiler.js), and the test runs it in Node.

import { compile, compileModule } from "svelte/compiler";
import { transform } from "sucrase";
import {
  buildEntrySource,
  describeError,
  EMPTY_BUNDLE_JS,
  type BuildResult,
  type ComponentDiagnostic,
} from "../userComponentEntry";
import { KIT_STYLES, RUNTIME_MODULES } from "./runtimeModules";

/** Reads a project file by absolute path; undefined when there is none. */
export type ReadFile = (path: string) => Promise<string | undefined>;

export type CompileOptions = {
  /** Text to compile instead of what is saved (an editor's unsaved buffer). */
  sources?: Record<string, string>;
};

/** The script, and the project files it was made from (a change to one
 * of them calls for a rebuild). */
export type CompileResult = BuildResult & { code: string; files: string[] };

type Module = { code: string; deps: Record<string, string> };

const ENTRY = "\0nx-user-components-entry";
const RUNTIME = new Set(RUNTIME_MODULES);
const STYLES = new Set<string>(KIT_STYLES);
const TRY_EXTENSIONS = ["", ".svelte", ".ts", ".js", ".svelte.ts", ".svelte.js", ".json", "/index.ts", "/index.js"];

const dirname = (p: string) => p.slice(0, p.lastIndexOf("/")) || "/";

/** a/b/../c -> a/c, for absolute posix paths. */
function normalize(p: string): string {
  const out: string[] = [];
  for (const seg of p.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") out.pop();
    else out.push(seg);
  }
  return "/" + out.join("/");
}

/** Every require("...") Sucrase's output makes, in order. */
function requires(code: string): string[] {
  const out: string[] = [];
  for (const m of code.matchAll(/\brequire\((["'])((?:(?!\1)[^\\\n])+)\1\)/g)) out.push(m[2]);
  return out;
}

// disableESTransforms: modern syntax (??, ?., class fields) is left as
// written; every browser the playground runs in has it.
const toCommonJs = (code: string, file: string) =>
  transform(code, { transforms: ["imports"], filePath: file, disableESTransforms: true }).code;

const stripTypes = (code: string, file: string) =>
  transform(code, { transforms: ["typescript"], filePath: file, disableESTransforms: true }).code;

/** One file, as CommonJS. */
function compileFile(path: string, text: string): string {
  if (path.endsWith(".svelte")) {
    return toCommonJs(compile(text, { filename: path, generate: "client", css: "injected" }).js.code, path);
  }
  if (/\.svelte\.(js|ts)$/.test(path)) {
    const js = path.endsWith(".ts") ? stripTypes(text, path) : text;
    return toCommonJs(compileModule(js, { filename: path, generate: "client" }).js.code, path);
  }
  if (path.endsWith(".ts")) {
    return transform(text, { transforms: ["typescript", "imports"], filePath: path, disableESTransforms: true }).code;
  }
  if (path.endsWith(".js") || path.endsWith(".mjs")) return toCommonJs(text, path);
  if (path.endsWith(".json")) return "module.exports = " + JSON.stringify(JSON.parse(text)) + ";";
  if (path.endsWith(".css")) {
    return `var s = document.createElement("style"); s.textContent = ${JSON.stringify(text)}; document.head.appendChild(s);`;
  }
  throw new Error(`${path}: the playground cannot load this kind of file`);
}

class Graph {
  readonly modules = new Map<string, Module>();
  private readonly svelteRoots = new Map<string, string | undefined>();

  constructor(
    private readonly read: ReadFile,
    private readonly sources: Record<string, string>
  ) {}

  private async text(path: string): Promise<string | undefined> {
    return path in this.sources ? this.sources[path] : this.read(path);
  }

  /** Where $lib points for a file: src/lib of the nearest folder above it
   * with a svelte.config.js, else of the folder holding its src/. */
  private async libFor(importer: string): Promise<string | undefined> {
    for (let dir = dirname(importer); ; dir = dirname(dir)) {
      if (!this.svelteRoots.has(dir)) {
        this.svelteRoots.set(dir, (await this.text(dir + "/svelte.config.js")) !== undefined ? dir : undefined);
      }
      const root = this.svelteRoots.get(dir);
      if (root) return root + "/src/lib";
      if (dir === "/") break;
    }
    const at = importer.lastIndexOf("/src/");
    return at >= 0 ? importer.slice(0, at) + "/src/lib" : undefined;
  }

  private async resolve(spec: string, importer: string): Promise<string> {
    if (RUNTIME.has(spec)) return "rt:" + spec;
    if (STYLES.has(spec)) return "none:";
    let base: string | undefined;
    if (spec.startsWith("./") || spec.startsWith("../")) base = normalize(dirname(importer) + "/" + spec);
    else if (spec.startsWith("/")) base = normalize(spec);
    else if (spec === "$lib" || spec.startsWith("$lib/")) {
      const lib = await this.libFor(importer);
      if (lib) base = normalize(lib + spec.slice(4));
    } else {
      throw new Error(
        `imports "${spec}", which the playground cannot load: here a component can import svelte, ` +
          "@joyautomation/nautilus-hmi and the project's own files"
      );
    }
    if (base) {
      for (const ext of TRY_EXTENSIONS) {
        if ((await this.text(base + ext)) !== undefined) return base + ext;
      }
    }
    throw new Error(`imports "${spec}", which is not in the project (from ${importer})`);
  }

  /** Adds path and everything it imports. Throws on the first failure. */
  async add(path: string, code?: string): Promise<void> {
    if (this.modules.has(path)) return;
    let compiled = code;
    if (compiled === undefined) {
      const text = await this.text(path);
      if (text === undefined) throw new Error(`${path} is not in the project`);
      compiled = compileFile(path, text);
    }
    const mod: Module = { code: compiled, deps: {} };
    this.modules.set(path, mod);
    try {
      for (const spec of requires(compiled)) {
        if (spec in mod.deps) continue;
        const target = await this.resolve(spec, path);
        mod.deps[spec] = target;
        if (!target.startsWith("rt:") && target !== "none:") await this.add(target);
      }
    } catch (err) {
      this.modules.delete(path);
      throw err;
    }
  }

  /** A copy, to try a component on without keeping what fails. */
  fork(): Graph {
    const g = new Graph(this.read, this.sources);
    for (const [k, v] of this.modules) g.modules.set(k, v);
    for (const [k, v] of this.svelteRoots) g.svelteRoots.set(k, v);
    return g;
  }
}

/** The linked script: each module a function in a registry, the entry run. */
function link(modules: Map<string, Module>): string {
  const defs = [...modules].map(
    ([id, m]) => `${JSON.stringify(id)}: [function (module, exports, require) {\n${m.code}\n}, ${JSON.stringify(m.deps)}]`
  );
  return `(function () {
var RT = window.__NX_RT__ || {};
var M = {
${defs.join(",\n")}
};
var C = {};
function load(id) {
  if (id === "none:") return {};
  if (id.slice(0, 3) === "rt:") {
    var m = RT[id.slice(3)];
    if (!m) throw new Error("the playground's runtime has no " + id.slice(3));
    return m;
  }
  var c = C[id];
  if (c) return c.exports;
  var d = M[id];
  var module = { exports: {} };
  C[id] = module;
  d[0].call(module.exports, module, module.exports, function (spec) {
    var to = d[1][spec];
    if (to === undefined) throw new Error("cannot resolve " + spec + " from " + id);
    return load(to);
  });
  return module.exports;
}
try {
  load(${JSON.stringify(ENTRY)});
} catch (e) {
  console.error("nautilus: user components", e);
  ${EMPTY_BUNDLE_JS.trim().replace(/\n/g, "\n  ")}
}
})();
`;
}

/**
 * Compiles components (name -> absolute .svelte path) and what they import
 * into one script. Never throws: a component that fails is left out with a
 * diagnostic, and with none left the script registers nothing.
 */
export async function compileComponents(
  components: Record<string, string>,
  read: ReadFile,
  options: CompileOptions = {}
): Promise<CompileResult> {
  const diagnostics: ComponentDiagnostic[] = [];
  let graph = new Graph(read, options.sources ?? {});
  const usable: [string, string][] = [];

  for (const [name, path] of Object.entries(components)) {
    const trial = graph.fork();
    try {
      await trial.add(path);
      graph = trial;
      usable.push([name, path]);
    } catch (err) {
      diagnostics.push({ component: name, message: describeError(err) });
    }
  }
  const files = () => [...graph.modules.keys()].filter((id) => id !== ENTRY);
  if (!usable.length) return { code: EMPTY_BUNDLE_JS, diagnostics, built: [], files: [] };

  try {
    const entry = compileModule(buildEntrySource(usable), { filename: "nx-user-components-entry.js", generate: "client" });
    await graph.add(ENTRY, toCommonJs(entry.js.code, "nx-user-components-entry.js"));
  } catch (err) {
    diagnostics.push({ component: "*", message: "compiling the user-components entry: " + describeError(err) });
    return { code: EMPTY_BUNDLE_JS, diagnostics, built: [], files: files() };
  }
  return { code: link(graph.modules), diagnostics, built: usable.map(([name]) => name), files: files() };
}
