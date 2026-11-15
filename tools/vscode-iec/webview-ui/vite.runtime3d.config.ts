import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { SCENE_MODULES, SVELTE_MODULES } from "../src/web/runtimeModules";

// The playground's 3D runtime (web build only): three and Threlte, added
// to window.__NX_RT__ for a part of a 3D scene shown in the component
// preview (src/componentPreviewLogic.ts, sceneHostSource). Its own file,
// loaded by the pane only for such a part: three is several times the
// size of everything else a custom component runs on.
//
// Svelte is not built in again: Threlte's components must run on the one
// in user-runtime.js (vite.runtime.config.ts), or the part and its canvas
// would not share a context. Every svelte import here reads that one.
const runtimeEntry: Plugin = {
  name: "nx-user-runtime-3d",
  enforce: "pre",
  resolveId: (id) => (id === "virtual:nx-user-runtime-3d" ? "\0virtual:nx-user-runtime-3d" : undefined),
  load(id) {
    if (id !== "\0virtual:nx-user-runtime-3d") return;
    const lines = SCENE_MODULES.map((m, i) => `import * as m${i} from ${JSON.stringify(m)};`);
    lines.push("const rt = (window.__NX_RT__ ??= {});");
    SCENE_MODULES.forEach((m, i) => lines.push(`rt[${JSON.stringify(m)}] = m${i};`));
    return lines.join("\n");
  },
};

const SVELTE = new Set<string>(SVELTE_MODULES);

export default defineConfig({
  // Threlte's few styles (the canvas fills its parent) travel in the
  // script: one file for the pane to load, and no stylesheet to forget.
  plugins: [svelte({ emitCss: false, compilerOptions: { css: "injected" } }), runtimeEntry],
  // A classic script has no import.meta: Threlte's extras resolve an inlined
  // (data:) wasm against import.meta.url, and an empty base throws.
  define: { "process.env.NODE_ENV": JSON.stringify("production"), "import.meta.url": "document.baseURI" },
  build: {
    outDir: "../media/dist",
    emptyOutDir: false,
    lib: { entry: "src/userRuntime3d.ts", formats: ["iife"], name: "NxUserRuntime3d", fileName: () => "user-runtime.3d.js" },
    rollupOptions: {
      external: (id) => SVELTE.has(id),
      output: {
        globals: (id) => `window.__NX_RT__[${JSON.stringify(id)}]`,
      },
    },
  },
});
