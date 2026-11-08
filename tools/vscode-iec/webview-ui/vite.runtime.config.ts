import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { createRequire } from "node:module";
import path from "node:path";
import { RUNTIME_MODULES } from "../src/web/runtimeModules";

// The playground's runtime for custom components (web build only): Svelte
// and the HMI kit, built once from the same svelte this webview builds
// with, published as window.__NX_RT__[specifier] for the components the
// extension host compiles in the browser (src/web/componentCompiler.ts).
// One Svelte instance, so the kit and a project's components share it.
//
// svelte/internal/flags/* are not run here: each switches a mode on for
// the whole runtime. They are getters that switch it on when a compiled
// component imports one, as its own import would.
const require = createRequire(import.meta.url);
const flagsIndex = path.join(path.dirname(require.resolve("svelte/package.json")), "src/internal/flags/index.js");
const FLAGS: Record<string, string> = {
  "svelte/internal/flags/legacy": "enable_legacy_mode_flag",
  "svelte/internal/flags/async": "enable_async_mode_flag",
  "svelte/internal/flags/tracing": "enable_tracing_mode_flag",
};

const runtimeEntry: Plugin = {
  name: "nx-user-runtime",
  enforce: "pre",
  resolveId: (id) => (id === "virtual:nx-user-runtime" ? "\0virtual:nx-user-runtime" : undefined),
  load(id) {
    if (id !== "\0virtual:nx-user-runtime") return;
    const plain = RUNTIME_MODULES.filter((m) => !(m in FLAGS));
    const lines = plain.map((m, i) => `import * as m${i} from ${JSON.stringify(m)};`);
    lines.push(`import { ${Object.values(FLAGS).join(", ")} } from ${JSON.stringify(flagsIndex)};`);
    lines.push("const rt = {};");
    plain.forEach((m, i) => lines.push(`rt[${JSON.stringify(m)}] = m${i};`));
    for (const [m, enable] of Object.entries(FLAGS)) {
      lines.push(`Object.defineProperty(rt, ${JSON.stringify(m)}, { enumerable: true, get() { ${enable}(); return {}; } });`);
    }
    lines.push("window.__NX_RT__ = rt;");
    return lines.join("\n");
  },
};

export default defineConfig({
  plugins: [svelte(), runtimeEntry],
  define: { "process.env.NODE_ENV": JSON.stringify("production") },
  build: {
    outDir: "../media/dist",
    emptyOutDir: false,
    lib: { entry: "src/userRuntime.ts", formats: ["iife"], name: "NxUserRuntime", fileName: () => "user-runtime.js" },
    rollupOptions: { output: { assetFileNames: "user-runtime.[ext]" } },
  },
});
