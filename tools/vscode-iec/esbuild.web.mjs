// The extension's web build (package.json "browser"): the same sources as
// the Node build, bundled for VS Code's web extension host, with the few
// modules that need a real machine swapped for browser ones (src/web/):
//
//   ./platform                     -> src/web/platform.ts   (IS_WEB, the playground's Run)
//   ./cli                          -> src/web/cli.ts        (naut is the playground engine)
//   ./userComponents               -> src/web/userComponents.ts (mimics draw the kit's built-ins)
//   child_process                  -> src/web/child_process.ts  (execFile(naut, ...) -> engine exec)
//   http, https                    -> src/web/http.ts       (the SSE stream -> engine stream)
//   vscode-languageclient/node     -> src/web/languageclient.ts (naut lsp in the engine)
//   path                           -> path-browserify
//   fs, os, net, zlib, crypto, module, esbuild, svelte/compiler -> src/web/stub.ts
//     (only real-machine features use them, and those are off: IS_WEB)
//
//   node esbuild.web.mjs [--watch]
import * as esbuild from "esbuild";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
const web = (f) => path.join(here, "src/web", f);

const swaps = {
  "child_process": web("child_process.ts"),
  "http": web("http.ts"),
  "https": web("http.ts"),
  "vscode-languageclient/node": web("languageclient.ts"),
  "path": require.resolve("path-browserify"),
};
const stubbed = ["fs", "fs/promises", "os", "net", "zlib", "crypto", "module", "esbuild", "svelte/compiler", "util", "stream", "events", "url"];

/** @type {esbuild.Plugin} */
const browserSwaps = {
  name: "nautilus-web-swaps",
  setup(build) {
    // Relative modules swapped whole: the platform switch, the CLI and the
    // mimic editor's user components.
    build.onResolve({ filter: /^\.\/(platform|cli|userComponents)$/ }, (args) => {
      if (args.importer.includes(`${path.sep}src${path.sep}web${path.sep}`)) return;
      return { path: web(args.path.slice(2) + ".ts") };
    });
    build.onResolve({ filter: /^(node:)?[a-z_][a-z0-9_./-]*$/ }, (args) => {
      const name = args.path.replace(/^node:/, "");
      if (swaps[name]) return { path: swaps[name] };
      if (stubbed.includes(name)) return { path: web("stub.ts") };
    });
  },
};

const options = {
  entryPoints: [path.join(here, "src/extension.ts")],
  outfile: path.join(here, "out/web/extension.js"),
  bundle: true,
  format: "cjs",
  platform: "browser",
  target: "es2022",
  external: ["vscode"],
  mainFields: ["browser", "module", "main"],
  sourcemap: true,
  minify: process.argv.includes("--minify"),
  plugins: [browserSwaps],
  // The web extension host has no `process`; the few reads of it (platform
  // checks, an env override) see an empty one.
  banner: { js: "var process = globalThis.process || { env: {}, platform: 'browser', versions: {}, cwd: () => '/project' };" },
  define: { "process.env.NODE_ENV": '"production"' },
  logLevel: "info",
};

// The custom-component compiler (src/web/componentCompiler.ts: Svelte's
// compiler and Sucrase, about 1 MB), a file of its own that
// src/web/userComponents.ts loads only when a mimic uses a custom
// component. Its svelte/compiler is webview-ui's, the same Svelte that
// media/dist/user-runtime.js (webview-ui/vite.runtime.config.ts) runs the
// compiled components on.
const webviewSvelte = path.join(here, "webview-ui", "node_modules", "svelte");
const compiler = {
  ...options,
  entryPoints: [web("componentCompiler.ts")],
  outfile: path.join(here, "out/web/component-compiler.js"),
  external: [],
  plugins: [],
  alias: { "svelte/compiler": path.join(webviewSvelte, "compiler", "index.js") },
};

if (process.argv.includes("--watch")) {
  for (const o of [options, compiler]) await (await esbuild.context(o)).watch();
} else {
  await Promise.all([esbuild.build(options), esbuild.build(compiler)]);
}
